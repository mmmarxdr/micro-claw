package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Compile-time interface assertion.
var _ StreamingProvider = (*OpenAIProvider)(nil)

// --------------------------------------------------------------------------
// Wire types — OpenAI streaming (OpenAI-compatible SSE delta format)
// --------------------------------------------------------------------------

// openaiStreamChunk is the JSON payload of a single SSE data frame
// during an OpenAI streaming response.
type openaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   *string                `json:"content"`
			ToolCalls []openaiStreamToolCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// openaiStreamToolCall represents a single tool call delta in a streaming chunk.
// The first chunk for an index carries ID and function.name; subsequent chunks
// carry only function.arguments increments.
type openaiStreamToolCall struct {
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

type openaiToolAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

// --------------------------------------------------------------------------
// ChatStream — streaming entry point
// --------------------------------------------------------------------------

// ChatStream initiates a streaming chat completion request to the OpenAI-compatible endpoint.
// It returns a StreamResult whose Events channel delivers incremental deltas.
func (p *OpenAIProvider) ChatStream(ctx context.Context, req ChatRequest) (*StreamResult, error) {
	// Step 1: Build messages array — same as Chat().
	msgs := make([]openaiMessage, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.SystemPrompt})
	}

	for _, m := range req.Messages {
		switch {
		case m.Role == "tool":
			msgs = append(msgs, openaiMessage{
				Role:       "tool",
				ToolCallID: m.ToolCallID,
				Content:    m.Content,
			})
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			msgs = append(msgs, openaiMessage{
				Role:      "assistant",
				Content:   nil,
				ToolCalls: mapOpenAIToolCallsToWire(m.ToolCalls),
			})
		default:
			// Emit parts array when the message contains media blocks;
			// fall back to a plain TextOnly string for text-only messages to
			// preserve backward compatibility with existing tests and wire format.
			if m.Content.HasMedia() {
				msgs = append(msgs, openaiMessage{
					Role:    m.Role,
					Content: p.translateBlocks(ctx, m.Content),
				})
			} else {
				msgs = append(msgs, openaiMessage{Role: m.Role, Content: m.Content.TextOnly()})
			}
		}
	}

	// Step 2: Build tools array.
	var tools []openaiTool
	for _, t := range req.Tools {
		var tool openaiTool
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
		streamModel = p.model
	}
	apiReq := struct {
		openaiRequest
		Stream bool `json:"stream"`
	}{
		openaiRequest: openaiRequest{
			Model:    streamModel,
			Messages: msgs,
			Tools:    tools,
		},
		Stream: true,
	}
	if req.MaxTokens > 0 {
		apiReq.MaxTokens = req.MaxTokens
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openai stream: marshaling request: %w", err)
	}

	// Step 4: Make HTTP request with a streaming client (no Timeout).
	url := p.baseURL + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openai stream: creating http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	streamClient := &http.Client{} // no Timeout — rely on context for cancellation
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai stream: %w", wrapNetworkError(err))
	}

	// Step 5: Check HTTP status before starting SSE parsing.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, classifyOpenAIError(resp.StatusCode, body)
	}

	// Step 6: Launch goroutine to parse SSE and emit StreamEvents.
	sr, events := NewStreamResult(32)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		var textContent strings.Builder
		var toolCalls []ToolCall
		accumulators := make(map[int]*openaiToolAccumulator)
		var inputTokens, outputTokens int
		var stopReason string

		parseErr := ParseSSE(resp.Body, func(ev SSEEvent) error {
			data := string(ev.Data)

			// Handle [DONE] sentinel — stop reading.
			if data == "[DONE]" {
				return errStreamDone
			}

			var chunk openaiStreamChunk
			if err := json.Unmarshal(ev.Data, &chunk); err != nil {
				events <- StreamEvent{
					Type: StreamEventError,
					Err:  fmt.Errorf("openai stream: parsing chunk: %w", err),
				}
				return fmt.Errorf("openai stream: parsing chunk: %w", err)
			}

			// Skip empty chunks (e.g. usage-only chunks without choices).
			if len(chunk.Choices) == 0 && chunk.Usage == nil {
				return nil
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
					acc = &openaiToolAccumulator{
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

				// Close any active tool calls.
				for idx, acc := range accumulators {
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
				Err:  fmt.Errorf("openai stream parse: %w", parseErr),
			}
			sr.SetResponse(nil, parseErr)
		} else {
			sr.SetResponse(assembled, nil)
		}
	}()

	return sr, nil
}
