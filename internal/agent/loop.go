package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/filter"
	"microagent/internal/provider"
	"microagent/internal/store"
	"microagent/internal/tool"
)

func userScope(channelID, senderID string) string {
	if senderID == "" {
		return channelID
	}
	return channelID + ":" + senderID
}

func (a *Agent) processMessage(ctx context.Context, msg channel.IncomingMessage) {
	slog.Debug("processing message",
		"channel_id", msg.ChannelID,
		"sender_id", msg.SenderID,
		"text_len", len(msg.Text),
	)

	scope := userScope(msg.ChannelID, msg.SenderID)
	convID := "conv_" + scope
	conv, err := a.store.LoadConversation(ctx, convID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("failed to load conversation, starting fresh", "id", convID, "error", err)
		}
		conv = &store.Conversation{
			ID:        convID,
			ChannelID: msg.ChannelID,
			CreatedAt: time.Now(),
		}
	}

	conv.Messages = append(conv.Messages, provider.ChatMessage{
		Role:    "user",
		Content: msg.Text,
	})

	// Token-based context management takes precedence when MaxContextTokens > 0.
	// Falls back to the legacy HistoryLength-based truncation when MaxContextTokens is 0.
	if a.config.MaxContextTokens > 0 {
		// Build a preliminary system prompt to estimate its token cost.
		preliminaryPrompt := a.config.Personality
		conv.Messages = a.manageContextTokens(ctx, preliminaryPrompt, conv.Messages)
	} else if a.config.HistoryLength > 0 && len(conv.Messages) > a.config.HistoryLength {
		// Legacy HistoryLength-based truncation (backward compatible).
		conv.Messages = a.legacyTruncate(ctx, conv.Messages)
	}

	memories, _ := a.store.SearchMemory(ctx, scope, msg.Text, a.config.MemoryResults)

	maxIters := a.config.MaxIterations
	if maxIters <= 0 {
		maxIters = 10
	}

	totalTimeout := a.limits.TotalTimeout
	if totalTimeout == 0 {
		totalTimeout = 120 * time.Second
	}
	loopCtx, cancelLoop := context.WithTimeout(ctx, totalTimeout)
	defer cancelLoop()

	// Detect streaming capabilities once before the loop.
	var streamingProv provider.StreamingProvider
	var streamSender channel.StreamSender
	if a.stream {
		if sp, ok := a.provider.(provider.StreamingProvider); ok {
			streamingProv = sp
		}
		if ss, ok := a.channel.(channel.StreamSender); ok {
			streamSender = ss
		} else {
			slog.Debug("streaming enabled but channel does not implement StreamSender; server-side streaming with buffered display")
		}
	}

	for i := 0; i < maxIters; i++ {
		req := a.buildContext(conv, memories)

		slog.Debug("calling LLM", "iteration", i, "messages", len(req.Messages))
		llmStart := time.Now()

		var resp *provider.ChatResponse
		var textStreamed bool

		if streamingProv != nil {
			resp, textStreamed, err = a.processStreamingCall(
				loopCtx, streamingProv, streamSender, req, msg.ChannelID, i, llmStart,
			)
		} else {
			resp, err = a.provider.Chat(loopCtx, req)
		}

		llmDuration := time.Since(llmStart)
		if err != nil {
			_ = a.auditor.Emit(ctx, audit.AuditEvent{
				ID: uuid.New().String(), ScopeID: scope,
				EventType: "llm_call", Timestamp: llmStart, DurationMs: llmDuration.Milliseconds(),
				Iteration: i, StopReason: "error",
			})
			slog.Error("provider chat failed", "error", err, "channel_id", msg.ChannelID)
			_ = a.channel.Send(ctx, channel.OutgoingMessage{
				ChannelID: msg.ChannelID,
				Text:      "⚠️ The AI provider returned an error. Please try again in a moment.",
			})
			return
		}
		_ = a.auditor.Emit(ctx, audit.AuditEvent{
			ID: uuid.New().String(), ScopeID: scope,
			EventType: "llm_call", Timestamp: llmStart, DurationMs: llmDuration.Milliseconds(),
			Model: a.config.Name, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			StopReason: resp.StopReason, Iteration: i,
		})

		// Send text to channel only if it wasn't already streamed.
		if resp.Content != "" && !textStreamed {
			_ = a.channel.Send(ctx, channel.OutgoingMessage{
				ChannelID: msg.ChannelID,
				Text:      resp.Content,
			})
		}

		if len(resp.ToolCalls) == 0 {
			slog.Debug("LLM responded with text", "response_len", len(resp.Content))
			conv.Messages = append(conv.Messages, provider.ChatMessage{
				Role:    "assistant",
				Content: resp.Content,
			})
			if resp.Content != "" {
				entry := store.MemoryEntry{
					ID:        uuid.New().String(),
					ScopeID:   scope,
					Content:   resp.Content,
					Source:    convID,
					CreatedAt: time.Now(),
				}
				if err := a.store.AppendMemory(ctx, scope, entry); err != nil {
					slog.Warn("failed to append memory", "error", err)
				} else {
					slog.Debug("memory appended", "scope_id", scope)
					if a.enricher != nil {
						a.enricher.Enqueue(entry)
					}
					// Async embedding — fire and forget.
					if a.embeddingWorker != nil {
						a.embeddingWorker.Enqueue(entry.ID, scope, entry.Content)
					}
				}
			}
			break
		}

		conv.Messages = append(conv.Messages, provider.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		slog.Debug("LLM requested tool calls", "count", len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			slog.Info("executing tool", "name", tc.Name, "id", tc.ID)
			t, ok := a.tools[tc.Name]

			var result tool.ToolResult
			toolStart := time.Now()
			if !ok {
				result = tool.ToolResult{IsError: true, Content: fmt.Sprintf("Tool %s not found", tc.Name)}
			} else {
				// Validate the LLM-generated input against the tool's JSON schema
				// before executing. This catches malformed JSON and missing required
				// fields early, avoiding panics or confusing errors inside tools.
				if validErr := validateToolInput(tc.Input, t.Schema()); validErr != nil {
					slog.Warn("tool input validation failed", "tool", tc.Name, "error", validErr)
					result = tool.ToolResult{IsError: true, Content: "invalid tool input: " + validErr.Error()}
				} else {
					toolTimeout := a.limits.ToolTimeout
					if toolTimeout == 0 {
						toolTimeout = 30 * time.Second
					}
					toolCtx, tCancel := context.WithTimeout(loopCtx, toolTimeout)
					result, err = executeWithRecover(toolCtx, t, tc.Input)
					tCancel()
					if err != nil {
						result = tool.ToolResult{IsError: true, Content: err.Error()}
					}
				}
			}

			var filterMetrics filter.Metrics
			if !result.IsError {
				result, filterMetrics = filter.Apply(tc.Name, tc.Input, result, a.filterCfg)
			}
			toolDuration := time.Since(toolStart)

			status := "success"
			if result.IsError {
				status = "error"
			}
			slog.Debug("tool execution complete", "name", tc.Name, "status", status, "result_len", len(result.Content))
			_ = a.auditor.Emit(ctx, audit.AuditEvent{
				ID: uuid.New().String(), ScopeID: scope,
				EventType: "tool_use", Timestamp: toolStart, DurationMs: toolDuration.Milliseconds(),
				ToolName: tc.Name, ToolOK: !result.IsError, Details: result.Meta,
				OriginalBytes: filterMetrics.OriginalBytes, CompressedBytes: filterMetrics.CompressedBytes,
				FilterName: filterMetrics.FilterName,
			})
			// Apply injection detection before wrapping, if enabled.
			resultContent := result.Content
			if config.BoolVal(a.filterCfg.InjectionDetection) {
				var injected bool
				resultContent, injected = filter.ApplyInjectionFilter(result.Content)
				if injected {
					slog.Warn("potential prompt injection detected in tool result", "tool", tc.Name)
				}
			}
			safeContent := html.EscapeString(resultContent)
			conv.Messages = append(conv.Messages, provider.ChatMessage{
				Role:       "tool",
				Content:    fmt.Sprintf("<tool_result status=\"%s\">\n%s\n</tool_result>", status, safeContent),
				ToolCallID: tc.ID,
			})
		}

		if i == maxIters-1 {
			_ = a.channel.Send(ctx, channel.OutgoingMessage{
				ChannelID: msg.ChannelID,
				Text:      "(iteration limit reached)",
			})
		}
	}

	conv.UpdatedAt = time.Now()
	_ = a.store.SaveConversation(ctx, *conv)
}

// legacyTruncate performs the original HistoryLength-based truncation with LLM summarization.
// Preserved for backward compatibility when MaxContextTokens is 0.
func (a *Agent) legacyTruncate(ctx context.Context, messages []provider.ChatMessage) []provider.ChatMessage {
	// Find the first user message before trimming
	firstUserIdx := -1
	for i, m := range messages {
		if m.Role == "user" {
			firstUserIdx = i
			break
		}
	}
	trim := len(messages) - a.config.HistoryLength
	discarded := messages[:trim]
	tail := messages[trim:]

	var sumText string
	if len(discarded) > 0 {
		summarizeCtx, cancelSum := context.WithTimeout(ctx, 30*time.Second)
		sumReq := provider.ChatRequest{
			SystemPrompt: "Provide a concise summary of the following conversation segment.",
			Messages:     discarded,
			MaxTokens:    500,
		}
		sumResp, err := a.provider.Chat(summarizeCtx, sumReq)
		cancelSum()
		if err == nil && sumResp != nil && sumResp.Content != "" {
			sumText = "(Summary of previous conversation):\n" + sumResp.Content
		}
	}

	if sumText != "" {
		summaryMsg := provider.ChatMessage{Role: "assistant", Content: sumText}
		tail = append([]provider.ChatMessage{summaryMsg}, tail...)
	}

	// Preserve the first user message if it was trimmed off
	if firstUserIdx >= 0 && firstUserIdx < trim {
		preserved := messages[firstUserIdx]
		tail = append([]provider.ChatMessage{preserved}, tail...)
	}
	return tail
}

func executeWithRecover(ctx context.Context, t tool.Tool, params json.RawMessage) (result tool.ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = tool.ToolResult{IsError: true, Content: fmt.Sprintf("Tool crashed: %v", r)}
			err = nil
		}
	}()
	return t.Execute(ctx, params)
}
