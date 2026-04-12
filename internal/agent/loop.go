package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/filter"
	"microagent/internal/provider"
	"microagent/internal/store"
	"microagent/internal/tool"
)

// isCronMessage returns true when a ChannelID was created by the cron scheduler
// (format: "cron:<job_id>"). Used to gate cron-specific error metadata.
func isCronMessage(channelID string) bool {
	return len(channelID) > 5 && channelID[:5] == "cron:"
}

func userScope(channelID, senderID string) string {
	if senderID == "" {
		return channelID
	}
	return channelID + ":" + senderID
}

func (a *Agent) processMessage(ctx context.Context, msg channel.IncomingMessage) {
	slog.Debug("incoming message",
		"block_count", len(msg.Content),
		"text_len", len(msg.Content.TextOnly()),
		"has_media", msg.Content.HasMedia(),
		"channel_id", msg.ChannelID,
	)

	// Slash command dispatch — intercept before LLM.
	if cmdText := msg.Content.TextOnly(); cmdText != "" {
		if name, args, isCmd := parseCommand(cmdText); isCmd {
			if handler, found := a.commands.Lookup(name); found {
				slog.Info("slash command dispatched", "command", name)
				cc := CommandContext{
					Ctx:          ctx,
					ChannelID:    msg.ChannelID,
					SenderID:     msg.SenderID,
					Args:         args,
					Store:        a.store,
					Config:       &a.config,
					Reply:        a.makeReply(ctx, msg.ChannelID),
					Registry:     a.commands,
					ProviderName: a.provider.Name(),
					ChannelName:  a.channelName,
					StartedAt:    a.startedAt,
					Inbox:        a.inbox,
				}
				if err := handler(cc); err != nil {
					slog.Error("command handler failed", "command", name, "error", err)
					cc.Reply("Command failed: " + err.Error())
				}
				return
			}
			// Unknown command — inform the user.
			a.makeReply(ctx, msg.ChannelID)("Unknown command /" + name + ". Type /help for available commands.")
			return
		}
	}

	// Detect telemetry capability once per message.
	telemetry, hasTelemetry := a.channel.(channel.TelemetryEmitter)
	if hasTelemetry {
		_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
			"type": "turn_start",
		})
	}
	turnStart := time.Now()
	var totalInputTokens, totalOutputTokens int

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
		Content: msg.Content,
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

	memories, _ := a.store.SearchMemory(ctx, scope, msg.Content.TextOnly(), a.config.MemoryResults)

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

	// Determine degradation once per turn, before the tool-call loop.
	// A degraded turn means the current provider cannot handle media blocks
	// in the user's message — we note it and prepend a notice to the final reply.
	degraded := !a.provider.SupportsMultimodal() && msg.Content.HasMedia()
	var degradedBlocks content.Blocks
	if degraded {
		degradedBlocks = msg.Content
		typesList := make([]string, 0, len(msg.Content))
		seen := map[string]bool{}
		for _, b := range msg.Content {
			if string(b.Type) != "text" && !seen[string(b.Type)] {
				typesList = append(typesList, string(b.Type))
				seen[string(b.Type)] = true
			}
		}
		slog.Info("degradation", "provider_name", a.provider.Name(), "block_types", typesList)
	}

	for i := 0; i < maxIters; i++ {
		req := a.buildContext(conv, memories)

		slog.Debug("calling LLM", "iteration", i, "messages", len(req.Messages))
		if hasTelemetry {
			label := "Thinking..."
			if i > 0 {
				label = fmt.Sprintf("Processing iteration %d...", i+1)
			}
			_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
				"type": "thinking",
				"text": label,
			})
		}
		llmStart := time.Now()

		var resp *provider.ChatResponse
		var textStreamed bool

		if streamingProv != nil {
			var te channel.TelemetryEmitter
			if hasTelemetry {
				te = telemetry
			}
			resp, textStreamed, err = a.processStreamingCall(
				loopCtx, streamingProv, streamSender, req, msg.ChannelID, i, llmStart, te,
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
			errMsg := channel.OutgoingMessage{
				ChannelID: msg.ChannelID,
				Text:      "The AI provider returned an error. Please try again in a moment.",
			}
			if isCronMessage(msg.ChannelID) {
				errMsg.Metadata = map[string]string{"cron_error": "true"}
			}
			_ = a.channel.Send(ctx, errMsg)
			return
		}
		_ = a.auditor.Emit(ctx, audit.AuditEvent{
			ID: uuid.New().String(), ScopeID: scope,
			EventType: "llm_call", Timestamp: llmStart, DurationMs: llmDuration.Milliseconds(),
			Model: a.config.Name, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			StopReason: resp.StopReason, Iteration: i,
		})
		totalInputTokens += resp.Usage.InputTokens
		totalOutputTokens += resp.Usage.OutputTokens
		if hasTelemetry {
			_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
				"type":          "status",
				"elapsed_ms":    time.Since(turnStart).Milliseconds(),
				"input_tokens":  resp.Usage.InputTokens,
				"output_tokens": resp.Usage.OutputTokens,
				"iteration":     i + 1,
			})
		}

		// Prepend degradation notice to the final text reply (no tool calls remaining).
		if degraded && len(resp.ToolCalls) == 0 {
			notice := content.DegradationNotice(degradedBlocks)
			if notice != "" {
				resp.Content = notice + "\n" + resp.Content
			}
		}

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
				Content: content.TextBlock(resp.Content),
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
			Content:   content.TextBlock(resp.Content),
			ToolCalls: resp.ToolCalls,
		})

		slog.Debug("LLM requested tool calls", "count", len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			slog.Info("executing tool", "name", tc.Name, "id", tc.ID)
			if hasTelemetry {
				_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
					"type":         "tool_start",
					"name":         tc.Name,
					"input":        string(tc.Input),
					"tool_call_id": tc.ID,
				})
			}
			t, ok := a.tools[tc.Name]

			var result tool.ToolResult
			toolStart := time.Now()
			skippedByPreApply := false
			if !ok {
				result = tool.ToolResult{IsError: true, Content: fmt.Sprintf("Tool %s not found", tc.Name)}
			} else {
				// Task 1: PreApply hook - call before tool execution when context_mode is enabled
				// If PreApply returns (result, true), skip execution and use the result directly
				if a.ctxModeCfg.Mode != config.ContextModeOff {
					if preResult, shouldSkip := filter.PreApply(loopCtx, tc.Name, tc.Input, a.ctxModeCfg); shouldSkip {
						result = preResult
						skippedByPreApply = true
						slog.Debug("tool execution skipped by PreApply", "tool", tc.Name)
					}
				}

				// Only execute if not skipped by PreApply
				if !skippedByPreApply {
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
			}

			var filterMetrics filter.Metrics
			if !result.IsError {
				result, filterMetrics = filter.Apply(tc.Name, tc.Input, result, a.filterCfg)
			}

			// Task 2: Auto-Index after execution - if enabled and result is not an error
			// Works for both normal execution and PreApply-intercepted execution
			if a.outputStore != nil && config.BoolVal(a.ctxModeCfg.AutoIndexOutputs) && !result.IsError {
				// Extract command from input for shell_exec tool
				var cmd string
				if tc.Name == "shell_exec" {
					var params struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal(tc.Input, &params); err == nil {
						cmd = params.Command
					}
				}

				// H2: read exit code from Meta set by PreApply; fall back to 0.
				exitCode := 0
				if ec, ok := result.Meta["microagent/exit_code"]; ok {
					if v, err := strconv.Atoi(ec); err == nil {
						exitCode = v
					}
				}

				// H3: read sandbox truncation flag from Meta set by PreApply;
				// fall back to the filter-level comparison when the key is absent.
				truncated := filterMetrics.CompressedBytes < filterMetrics.OriginalBytes
				if tv, ok := result.Meta["microagent/truncated"]; ok {
					truncated = tv == "true"
				}

				// Only index non-empty outputs to avoid noisy warnings for commands that
				// succeed with no stdout (e.g. `touch foo`).
				if result.Content != "" {
					output := store.ToolOutput{
						ID:        tc.ID,
						ToolName:  tc.Name,
						Command:   cmd,
						Content:   result.Content,
						Truncated: truncated,
						ExitCode:  exitCode,
						Timestamp: time.Now().UTC(),
					}
					if a.indexWorker != nil {
						a.indexWorker.Enqueue(output)
					} else {
						// Fallback: synchronous indexing when worker is unavailable.
						if err := a.outputStore.IndexOutput(ctx, output); err != nil {
							slog.Warn("failed to index tool output", "tool", tc.Name, "error", err)
						}
					}
				}
			}

			toolDuration := time.Since(toolStart)

			status := "success"
			if result.IsError {
				status = "error"
			}
			slog.Debug("tool execution complete", "name", tc.Name, "status", status, "result_len", len(result.Content))
			if hasTelemetry {
				_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
					"type":         "tool_done",
					"name":         tc.Name,
					"output":       truncateTelemetry(result.Content, 500),
					"tool_call_id": tc.ID,
					"duration_ms":  toolDuration.Milliseconds(),
					"is_error":     result.IsError,
				})
			}
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
				Content:    content.TextBlock(fmt.Sprintf("<tool_result status=\"%s\">\n%s\n</tool_result>", status, safeContent)),
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

	if hasTelemetry {
		_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
			"type":                "turn_end",
			"elapsed_ms":          time.Since(turnStart).Milliseconds(),
			"total_input_tokens":  totalInputTokens,
			"total_output_tokens": totalOutputTokens,
			"iterations":          maxIters,
		})
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
		summaryMsg := provider.ChatMessage{Role: "assistant", Content: content.TextBlock(sumText)}
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

// truncateTelemetry truncates s to at most maxLen bytes, appending "…" if cut.
// Used for tool output in telemetry frames to keep payloads small.
func truncateTelemetry(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
