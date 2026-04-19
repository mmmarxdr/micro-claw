package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// errStreamDone is a sentinel error returned from the SSE callback to signal
// that the [DONE] marker was received and parsing should stop.
var errStreamDone = errors.New("stream done")

// Compile-time interface assertion.
var _ StreamingProvider = (*OpenRouterProvider)(nil)

// --------------------------------------------------------------------------
// Wire types — OpenRouter streaming (OpenAI-compatible SSE delta format)
// --------------------------------------------------------------------------

// openrouterStreamChunk is the JSON payload of a single SSE data frame
// during an OpenRouter streaming response.
type openrouterStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          *string                    `json:"content"`
			Reasoning        *string                    `json:"reasoning,omitempty"`         // OpenRouter reasoning field
			ReasoningContent *string                    `json:"reasoning_content,omitempty"` // DeepSeek variant
			ToolCalls        []openrouterStreamToolCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// openrouterStreamToolCall represents a single tool call delta in a streaming chunk.
// The first chunk for an index carries ID and function.name; subsequent chunks
// carry only function.arguments increments.
type openrouterStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// --------------------------------------------------------------------------
// Tool call accumulator — tracks in-flight tool calls across SSE chunks
// --------------------------------------------------------------------------

type openrouterToolAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

// --------------------------------------------------------------------------
// ChatStream — streaming entry point
// --------------------------------------------------------------------------

// ChatStream initiates a streaming chat completion request to OpenRouter.
// It returns a StreamResult whose Events channel delivers incremental deltas.
func (p *OpenRouterProvider) ChatStream(ctx context.Context, req ChatRequest) (*StreamResult, error) {
	// Step 1: Build messages array — same as Chat().
	msgs := make([]openrouterMessage, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		msgs = append(msgs, openrouterMessage{Role: "system", Content: req.SystemPrompt})
	}

	for _, m := range req.Messages {
		switch {
		case m.Role == "tool":
			msgs = append(msgs, openrouterMessage{
				Role:       "tool",
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			msgs = append(msgs, openrouterMessage{
				Role:      "assistant",
				Content:   nil,
				ToolCalls: mapToolCallsToWire(m.ToolCalls),
			})
		default:
			msgs = append(msgs, openrouterMessage{Role: m.Role, Content: m.Content})
		}
	}

	// Step 2: Build tools array.
	var tools []openrouterTool
	for _, t := range req.Tools {
		var tool openrouterTool
		tool.Type = "function"
		tool.Function.Name = t.Name
		tool.Function.Description = t.Description
		tool.Function.Parameters = t.InputSchema
		tools = append(tools, tool)
	}

	// Step 3: Marshal request with stream: true.
	// Per-request model override takes precedence over the provider's configured model.
	streamModel := req.Model
	if streamModel == "" {
		streamModel = p.config.Model
	}
	orReq := openrouterRequest{
		Model:    streamModel,
		Messages: msgs,
		Tools:    tools,
	}
	if req.MaxTokens > 0 {
		orReq.MaxTokens = req.MaxTokens
	}

	// Check model capability for reasoning activation (Phase 3.2 / ADR-7).
	if p.modelInfoStore != nil {
		if info, ok := p.modelInfoStore.GetModelInfo(streamModel); ok {
			for _, param := range info.SupportedParameters {
				if param == "reasoning" || param == "include_reasoning" {
					orReq.IncludeReasoning = true
					break
				}
			}
		}
	}

	apiReq := struct {
		openrouterRequest
		Stream bool `json:"stream"`
	}{
		openrouterRequest: orReq,
		Stream:            true,
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter stream: marshaling request: %w", err)
	}

	// Step 4: Make HTTP request with a streaming client (no Timeout).
	url := p.baseURL + "/api/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openrouter stream: creating http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	streamClient := &http.Client{} // no Timeout — rely on context for cancellation
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter stream: %w", wrapNetworkError(err))
	}

	// Step 5: Check HTTP status before starting SSE parsing.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, classifyOpenRouterError(resp.StatusCode, body)
	}

	// Step 6: Launch goroutine to parse SSE and emit StreamEvents.
	sr, events := NewStreamResult(32)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		var textContent strings.Builder
		var toolCalls []ToolCall
		accumulators := make(map[int]*openrouterToolAccumulator)
		var inputTokens, outputTokens int
		var stopReason string

		parseErr := ParseSSE(resp.Body, func(ev SSEEvent) error {
			data := string(ev.Data)

			// Handle [DONE] sentinel — stop reading.
			if data == "[DONE]" {
				return errStreamDone
			}

			var chunk openrouterStreamChunk
			if err := json.Unmarshal(ev.Data, &chunk); err != nil {
				events <- StreamEvent{
					Type: StreamEventError,
					Err:  fmt.Errorf("openrouter stream: parsing chunk: %w", err),
				}
				return fmt.Errorf("openrouter stream: parsing chunk: %w", err)
			}

			// Check for API error in the chunk (non-standard but some proxies do this).
			if len(chunk.Choices) == 0 && chunk.Usage == nil {
				return nil // empty chunk — skip
			}

			// Extract usage if present.
			if chunk.Usage != nil {
				inputTokens = chunk.Usage.PromptTokens
				outputTokens = chunk.Usage.CompletionTokens
			}

			if len(chunk.Choices) == 0 {
				return nil
			}

			choice := chunk.Choices[0]

			// Reasoning deltas — emitted before text delta; never accumulated into content.
			if choice.Delta.Reasoning != nil && *choice.Delta.Reasoning != "" {
				events <- StreamEvent{
					Type: StreamEventReasoningDelta,
					Text: *choice.Delta.Reasoning,
				}
			}
			if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
				events <- StreamEvent{
					Type: StreamEventReasoningDelta,
					Text: *choice.Delta.ReasoningContent,
				}
			}

			// Text delta.
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				textContent.WriteString(*choice.Delta.Content)
				events <- StreamEvent{
					Type: StreamEventTextDelta,
					Text: *choice.Delta.Content,
				}
			}

			// Tool call deltas.
			for _, tc := range choice.Delta.ToolCalls {
				acc, exists := accumulators[tc.Index]
				if !exists {
					// First chunk for this tool call index — emit ToolCallStart.
					acc = &openrouterToolAccumulator{
						id:   tc.ID,
						name: tc.Function.Name,
					}
					accumulators[tc.Index] = acc
					events <- StreamEvent{
						Type:       StreamEventToolCallStart,
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					}
				}

				// Accumulate arguments.
				if tc.Function.Arguments != "" {
					acc.arguments.WriteString(tc.Function.Arguments)
					events <- StreamEvent{
						Type:      StreamEventToolCallDelta,
						ToolInput: tc.Function.Arguments,
					}
				}
			}

			// Finish reason.
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				stopReason = normalizeFinishReason(*choice.FinishReason)

				// Close any active tool calls in deterministic index order.
				indices := make([]int, 0, len(accumulators))
				for idx := range accumulators {
					indices = append(indices, idx)
				}
				sort.Ints(indices)
				for _, idx := range indices {
					acc := accumulators[idx]
					toolCalls = append(toolCalls, assembleToolCall(
						acc.id, acc.name, acc.arguments.String(),
					))
					events <- StreamEvent{Type: StreamEventToolCallEnd}
					delete(accumulators, idx)
				}

				// Emit usage event.
				events <- StreamEvent{
					Type: StreamEventUsage,
					Usage: &UsageStats{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					},
					StopReason: stopReason,
				}

				// Emit done event.
				events <- StreamEvent{Type: StreamEventDone}
			}

			return nil
		})

		// Assemble final response.
		assembled := &ChatResponse{
			Content:    textContent.String(),
			ToolCalls:  toolCalls,
			StopReason: stopReason,
			Usage: UsageStats{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		}

		// errStreamDone is expected — it means [DONE] was received.
		if parseErr != nil && !errors.Is(parseErr, errStreamDone) {
			events <- StreamEvent{
				Type: StreamEventError,
				Err:  fmt.Errorf("openrouter stream parse: %w", parseErr),
			}
			sr.SetResponse(nil, parseErr)
		} else {
			sr.SetResponse(assembled, nil)
		}
	}()

	return sr, nil
}
