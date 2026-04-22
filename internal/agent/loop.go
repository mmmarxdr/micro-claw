package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/filter"
	"daimon/internal/notify"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
	"daimon/internal/store"
	"daimon/internal/tool"
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

	if a.bus != nil {
		a.bus.Emit(notify.Event{
			Type:      notify.EventTurnStarted,
			Origin:    notify.OriginAgent,
			ChannelID: msg.ChannelID,
			Timestamp: time.Now(),
		})
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

	// Continuation requests re-enter the loop against the existing conv —
	// no new user message, no fresh memory/RAG search. The conversation
	// already holds the tool results and assistant state from the turn
	// that hit the iteration cap.
	if !msg.IsContinuation {
		// Content carries full Blocks (text + media) — do not flatten here.
		conv.Messages = append(conv.Messages, provider.ChatMessage{
			Role:    "user",
			Content: msg.Content,
		})
	}

	// Context management via ContextManager (smart, legacy, or none strategy).
	// The ContextManager is always present after New() — strategy controls behavior.
	memories, _ := a.store.SearchMemory(ctx, scope, msg.Content.TextOnly(), a.config.MemoryResults)

	// RAG: search for relevant document chunks when a DocumentStore is wired.
	// When HyDE is enabled (and a hypothesis function is provided), we run a
	// 3-way Reciprocal Rank Fusion merge across raw-BM25, hyde-BM25, and
	// hyde-cosine lists. On any HyDE failure we fall through to the baseline
	// path — retrieval must never fail.
	var ragResults []rag.SearchResult
	if a.ragStore != nil {
		queryText := msg.Content.TextOnly()
		var queryVec []float32
		if a.ragEmbedFn != nil {
			if vec, err := a.ragEmbedFn(ctx, queryText); err == nil {
				queryVec = vec
			}
		}
		limit := a.ragMaxChunks
		if limit <= 0 {
			limit = 5
		}
		searchOpts := rag.SearchOptions{
			Limit:          limit,
			NeighborRadius: a.ragRetrievalConf.NeighborRadius,
			MaxBM25Score:   a.ragRetrievalConf.MaxBM25Score,
			MinCosineScore: a.ragRetrievalConf.MinCosineScore,
		}

		totalStart := time.Now()
		hydeEnabled := a.ragHydeConf.Enabled && a.ragHypothesisFn != nil
		if hydeEnabled {
			// ragSearchWithHyDE delegates to rag.PerformHydeSearch which handles
			// both the HyDE path and fallthrough-to-baseline internally.
			// We trust it to always produce correct results — no re-run needed.
			ragResults = a.ragSearchWithHyDE(ctx, queryText, queryVec, searchOpts)
		}

		// Baseline path: HyDE is disabled. PerformHydeSearch handles the
		// disabled path itself when called via ragSearchWithHyDE, so this
		// branch only runs when hydeEnabled=false (direct SearchChunks call).
		usedBaseline := !hydeEnabled
		if usedBaseline {
			if results, err := a.ragStore.SearchChunks(ctx, queryText, queryVec, searchOpts); err == nil {
				ragResults = results
			}
		}

		// Record a baseline metrics event when HyDE was not used.
		// The HyDE path (and its fallthrough) record their own events inside
		// PerformHydeSearch, so we only record here for the pure-baseline case.
		if usedBaseline && a.ragMetrics != nil {
			prov := map[string]int{}
			for range ragResults {
				prov["raw-bm25"]++
			}
			a.ragMetrics.Record(metrics.Event{
				Timestamp:           time.Now(),
				Query:               queryText,
				TotalDurationMs:     time.Since(totalStart).Milliseconds(),
				BM25Hits:            len(ragResults),
				HydeEnabled:         false,
				FinalChunksReturned: len(ragResults),
				ProvenanceBreakdown: prov,
			})
		}
	}

	systemPrompt := a.buildSystemPrompt(memories, ragResults)
	toolDefs := a.buildToolDefs()
	conv.Messages = a.contextMgr.Manage(ctx, systemPrompt, toolDefs, conv.Messages)

	// MaxIterations=0 (default) means "no hard cap" — the turn is bounded
	// by limits.total_timeout and the token budget below. A positive value
	// surfaces a "continue" pill in the UI when crossed.
	maxIters := a.config.MaxIterations
	hasIterCap := maxIters > 0
	if !hasIterCap {
		maxIters = math.MaxInt32
	}
	// Continuation with the "unlimited" choice lifts whatever cap was set
	// for THIS turn only — still bounded by total_timeout.
	if msg.IsContinuation && msg.Unlimited {
		maxIters = math.MaxInt32
		hasIterCap = false
	}

	// Cumulative token budget for the whole turn. 0 = unlimited.
	maxTotalTokens := a.config.MaxTotalTokens
	// Continuation with Unlimited also waives the token budget for this turn.
	if msg.IsContinuation && msg.Unlimited {
		maxTotalTokens = 0
	}

	// Loop-detection ring buffer: the last `loopWindow` tool invocations of
	// this turn. When the same (name, input) appears at least `loopThreshold`
	// times in the window, we emit a one-shot `loop_detected` telemetry event
	// so the UI can surface a "Daimon seems to be repeating itself" warning.
	// Non-breaking — the agent keeps running; this is a heads-up, not a halt.
	const loopWindow = 5
	const loopThreshold = 3
	type recentCall struct{ name, input string }
	recentTools := make([]recentCall, 0, loopWindow)
	loopAlerted := make(map[string]bool)

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

	// lastRespContent captures the final LLM text for the turn.completed event.
	var lastRespContent string

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
		req := provider.ChatRequest{
			SystemPrompt: systemPrompt,
			Messages:     conv.Messages,
			Tools:        toolDefs,
			MaxTokens:    a.config.MaxTokensPerTurn,
			Temperature:  0.0,
		}

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
			stopReason := "error"
			if errors.Is(err, context.DeadlineExceeded) {
				stopReason = "turn_timeout"
			}
			_ = a.auditor.Emit(ctx, audit.AuditEvent{
				ID: uuid.New().String(), ScopeID: scope,
				EventType: "llm_call", Timestamp: llmStart, DurationMs: llmDuration.Milliseconds(),
				Iteration: i, StopReason: stopReason,
			})
			if stopReason == "turn_timeout" {
				slog.Warn("turn timeout exceeded", "limit", totalTimeout, "iteration", i, "channel_id", msg.ChannelID)
				if hasTelemetry {
					_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
						"type":            "turn_timeout_reached",
						"limit_seconds":   int(totalTimeout.Seconds()),
						"iteration":       i,
						"conversation_id": conv.ID,
					})
				} else {
					_ = a.channel.Send(ctx, channel.OutgoingMessage{
						ChannelID: msg.ChannelID,
						Text: fmt.Sprintf(
							"(turn aborted — %s total-time limit reached. Raise `limits.total_timeout` in config if you need longer turns.)",
							totalTimeout,
						),
					})
				}
				return
			}
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
			Model: a.provider.Model(), InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
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
			lastRespContent = resp.Content
			conv.Messages = append(conv.Messages, provider.ChatMessage{
				Role:    "assistant",
				Content: content.TextBlock(resp.Content),
			})
			if resp.Content != "" {
				if a.curator != nil {
					// Smart memory: Curator classifies, deduplicates, and selectively persists.
					userText := msg.Content.TextOnly()
					if curateErr := a.curator.Curate(ctx, scope, userText, resp.Content, convID); curateErr != nil {
						slog.Warn("memory curation failed, falling back to raw save", "error", curateErr)
						// Fallback: save unconditionally (legacy behaviour).
						entry := store.MemoryEntry{
							ID:         uuid.New().String(),
							ScopeID:    scope,
							Content:    resp.Content,
							Source:     convID,
							Importance: 5,
							CreatedAt:  time.Now(),
						}
						_ = a.store.AppendMemory(ctx, scope, entry)
					}
				} else {
					// Legacy path: save every response unconditionally.
					entry := store.MemoryEntry{
						ID:         uuid.New().String(),
						ScopeID:    scope,
						Content:    resp.Content,
						Source:     convID,
						Importance: 5,
						CreatedAt:  time.Now(),
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

			// Track this call + check whether the same (name, input) has
			// appeared `loopThreshold` times in the last `loopWindow` calls.
			// The input JSON itself is the de-facto hash — cheap, accurate,
			// and avoids a crypto import for a small window.
			call := recentCall{name: tc.Name, input: string(tc.Input)}
			recentTools = append(recentTools, call)
			if len(recentTools) > loopWindow {
				recentTools = recentTools[len(recentTools)-loopWindow:]
			}
			alertKey := tc.Name + "\x00" + string(tc.Input)
			if !loopAlerted[alertKey] {
				reps := 0
				for _, c := range recentTools {
					if c.name == call.name && c.input == call.input {
						reps++
					}
				}
				if reps >= loopThreshold {
					loopAlerted[alertKey] = true
					slog.Warn("loop detected", "tool", tc.Name, "repetitions", reps)
					if hasTelemetry {
						_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, map[string]any{
							"type":            "loop_detected",
							"tool_name":       tc.Name,
							"repetitions":     reps,
							"sample_input":    truncateTelemetry(string(tc.Input), 200),
							"conversation_id": conv.ID,
						})
					}
				}
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
						toolCtx = tool.WithScope(toolCtx, scope)
						result, err = executeWithRecover(toolCtx, t, tc.Input)
						tCancel()
						if err != nil {
							result = tool.ToolResult{
								IsError: true,
								Content: formatToolError(tc.Name, toolTimeout, err),
							}
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
				if ec, ok := result.Meta["daimon/exit_code"]; ok {
					if v, err := strconv.Atoi(ec); err == nil {
						exitCode = v
					}
				}

				// H3: read sandbox truncation flag from Meta set by PreApply;
				// fall back to the filter-level comparison when the key is absent.
				truncated := filterMetrics.CompressedBytes < filterMetrics.OriginalBytes
				if tv, ok := result.Meta["daimon/truncated"]; ok {
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
			// Wrap in CDATA so the LLM receives content verbatim (no HTML entity corruption).
			// "]]>" must be escaped inside CDATA by splitting the section.
			cdataContent := strings.ReplaceAll(resultContent, "]]>", "]]]]><![CDATA[>")
			conv.Messages = append(conv.Messages, provider.ChatMessage{
				Role:       "tool",
				Content:    content.TextBlock(fmt.Sprintf("<tool_result status=\"%s\"><![CDATA[%s]]></tool_result>", status, cdataContent)),
				ToolCallID: tc.ID,
			})
		}

		// End-of-iteration pause check. Two reasons can pause the loop and
		// surface a "continue" pill in the UI: a user-configured iteration
		// cap, or a user-configured cumulative-token budget. A single break
		// handles both — the legacy text fallback only fires on channels
		// that don't implement TelemetryEmitter.
		var pauseReason string
		switch {
		case hasIterCap && i == maxIters-1:
			pauseReason = "iteration_limit_reached"
		case maxTotalTokens > 0 && totalInputTokens+totalOutputTokens >= maxTotalTokens:
			pauseReason = "token_budget_reached"
		}
		if pauseReason != "" {
			if hasTelemetry {
				frame := map[string]any{
					"type":            pauseReason,
					"conversation_id": conv.ID,
				}
				switch pauseReason {
				case "iteration_limit_reached":
					frame["iterations"] = maxIters
				case "token_budget_reached":
					frame["consumed_tokens"] = totalInputTokens + totalOutputTokens
					frame["budget"] = maxTotalTokens
				}
				_ = telemetry.EmitTelemetry(ctx, msg.ChannelID, frame)
			} else {
				legacy := "(iteration limit reached)"
				if pauseReason == "token_budget_reached" {
					legacy = fmt.Sprintf("(token budget reached: %d/%d tokens)", totalInputTokens+totalOutputTokens, maxTotalTokens)
				}
				_ = a.channel.Send(ctx, channel.OutgoingMessage{
					ChannelID: msg.ChannelID,
					Text:      legacy,
				})
			}
			break
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

	if a.bus != nil {
		a.bus.Emit(notify.Event{
			Type:      notify.EventTurnCompleted,
			Origin:    notify.OriginAgent,
			ChannelID: msg.ChannelID,
			Text:      lastRespContent,
			Timestamp: time.Now(),
			Meta: map[string]string{
				"input_tokens":  fmt.Sprintf("%d", totalInputTokens),
				"output_tokens": fmt.Sprintf("%d", totalOutputTokens),
			},
		})
	}
}

// legacyTruncate performs the original HistoryLength-based truncation with LLM summarization.
// Preserved for backward compatibility when MaxContextTokens is 0.
//
// Deprecated: This function is still actively called by ContextManager.legacyManage when
// strategy == "legacy". It is wired into ContextManager.legacyFn in Agent.New(). Do NOT
// remove until the "legacy" strategy path is retired. See context_manager.go for the
// current smart compaction path.
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

// formatToolError converts an executor error into actionable copy for the LLM.
// Generic Go errors like "context deadline exceeded" don't tell the model what
// to do next — it often retries the same call and times out again. We rewrite
// timeouts into an explicit instruction NOT to retry, and surface
// cancellations as their own state so the model doesn't blame the tool.
func formatToolError(toolName string, timeout time.Duration, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf(
			"Tool %q exceeded the %s timeout. The previous approach is hanging or doing too much work. "+
				"Try a different command, narrow the scope (smaller path, fewer files, more specific filter), or use a different tool. "+
				"DO NOT retry the same call — it will time out again.",
			toolName, timeout,
		)
	case errors.Is(err, context.Canceled):
		return fmt.Sprintf("Tool %q was cancelled before completing (turn deadline or user cancel).", toolName)
	default:
		return err.Error()
	}
}

// ragSearchWithHyDE performs the HyDE-augmented retrieval path by delegating
// to rag.PerformHydeSearch — the single source of truth for HyDE retrieval.
//
// The agent loop constructs HydeSearchDeps from its own fields and calls
// PerformHydeSearch. On fallthrough (error, empty hypothesis, zero vector),
// PerformHydeSearch returns baseline results directly — the loop's existing
// nil-check triggers a redundant baseline call only when results are nil,
// but PerformHydeSearch always returns non-nil on fallthrough.
//
// Never returns an error — retrieval must not fail.
func (a *Agent) ragSearchWithHyDE(
	ctx context.Context,
	queryText string,
	queryVec []float32,
	searchOpts rag.SearchOptions,
) []rag.SearchResult {
	hydeCfg := rag.HydeSearchConfig{
		Enabled:           a.ragHydeConf.Enabled,
		HypothesisTimeout: a.ragHydeConf.HypothesisTimeout,
		QueryWeight:       a.ragHydeConf.QueryWeight,
		MaxCandidates:     a.ragHydeConf.MaxCandidates,
	}
	retrieval := rag.RetrievalSearchConfig{
		Limit:          searchOpts.Limit,
		NeighborRadius: searchOpts.NeighborRadius,
		MaxBM25Score:   searchOpts.MaxBM25Score,
		MinCosineScore: searchOpts.MinCosineScore,
	}

	// Provide a pre-computed queryVec via a thin closure so PerformHydeSearch
	// doesn't re-embed the raw query (the loop already computed it above).
	embedFn := a.ragEmbedFn
	if queryVec != nil && embedFn != nil {
		originalEmbed := embedFn
		embedFn = func(ctx2 context.Context, text string) ([]float32, error) {
			if text == queryText {
				return queryVec, nil
			}
			return originalEmbed(ctx2, text)
		}
	}

	deps := rag.HydeSearchDeps{
		Store:         a.ragStore,
		HypothesisFn:  a.ragHypothesisFn,
		EmbedFn:       embedFn,
		HydeConf:      hydeCfg,
		RetrievalConf: retrieval,
		Recorder:      a.ragMetrics,
	}

	results, _ := rag.PerformHydeSearch(ctx, queryText, deps)
	// PerformHydeSearch always returns results (never nil) even on fallthrough.
	// Return them directly; the caller's nil-check will not trigger.
	return results
}
