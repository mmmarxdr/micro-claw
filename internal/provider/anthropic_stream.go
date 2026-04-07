package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// --------------------------------------------------------------------------
// Anthropic SSE wire types — used only for streaming JSON deserialization
// --------------------------------------------------------------------------

type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Message      *anthropicStreamMsg    `json:"message,omitempty"`
	Index        int                    `json:"index"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
	Delta        *anthropicDelta        `json:"delta,omitempty"`
	Usage        *anthropicStreamUsage  `json:"usage,omitempty"`
	Error        *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type anthropicStreamMsg struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"` // "text" or "tool_use"
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}

type anthropicDelta struct {
	Type        string `json:"type"` // "text_delta", "input_json_delta", or message_delta fields
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicStreamUsage struct {
	OutputTokens int `json:"output_tokens"`
}

// anthropicStreamRequest extends anthropicRequest with the Stream field.
type anthropicStreamRequest struct {
	anthropicRequest
	Stream bool `json:"stream"`
}

// --------------------------------------------------------------------------
// ChatStream — streaming implementation for Anthropic
// --------------------------------------------------------------------------

// ChatStream implements StreamingProvider for Anthropic.
// It builds the same request as Chat() but adds "stream": true,
// then reads SSE events and maps them to StreamEvent values.
func (p *AnthropicProvider) ChatStream(ctx context.Context, req ChatRequest) (*StreamResult, error) {
	// 1. Build the same request body as Chat(), but with stream: true.
	apiReq := p.buildAnthropicRequest(req)
	streamReq := anthropicStreamRequest{
		anthropicRequest: apiReq,
		Stream:           true,
	}

	bodyBytes, err := json.Marshal(streamReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: marshaling request: %w", err)
	}

	url := "https://api.anthropic.com/v1/messages"
	if p.config.BaseURL != "" {
		url = p.config.BaseURL
	}

	// 2. Make HTTP request — use a separate client with NO Timeout for streaming.
	//    Context provides cancellation instead.
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: creating request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.config.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	streamClient := &http.Client{} // no Timeout — context cancellation handles it
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic stream: %w", wrapNetworkError(err))
	}

	// 3. Check HTTP status BEFORE starting to parse SSE.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, classifyAnthropicError(resp.StatusCode, body)
	}

	// 4. Launch goroutine to parse SSE and emit StreamEvents.
	sr, events := NewStreamResult(32)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		// Track state for response assembly.
		var textContent strings.Builder
		var toolCalls []ToolCall
		var currentToolInput strings.Builder
		var currentToolID, currentToolName string
		var inToolBlock bool
		var assembled ChatResponse

		parseErr := ParseSSE(resp.Body, func(ev SSEEvent) error {
			var sev anthropicStreamEvent
			if err := json.Unmarshal(ev.Data, &sev); err != nil {
				return fmt.Errorf("parsing SSE data: %w", err)
			}

			switch sev.Type {
			case "message_start":
				if sev.Message != nil {
					assembled.Usage.InputTokens = sev.Message.Usage.InputTokens
				}

			case "content_block_start":
				if sev.ContentBlock != nil && sev.ContentBlock.Type == "tool_use" {
					inToolBlock = true
					currentToolID = sev.ContentBlock.ID
					currentToolName = sev.ContentBlock.Name
					currentToolInput.Reset()
					events <- StreamEvent{
						Type:       StreamEventToolCallStart,
						ToolCallID: currentToolID,
						ToolName:   currentToolName,
					}
				}
				// text content_block_start is ignored — text comes in deltas

			case "content_block_delta":
				if sev.Delta != nil {
					switch sev.Delta.Type {
					case "text_delta":
						textContent.WriteString(sev.Delta.Text)
						events <- StreamEvent{
							Type: StreamEventTextDelta,
							Text: sev.Delta.Text,
						}
					case "input_json_delta":
						currentToolInput.WriteString(sev.Delta.PartialJSON)
						events <- StreamEvent{
							Type:      StreamEventToolCallDelta,
							ToolInput: sev.Delta.PartialJSON,
						}
					}
				}

			case "content_block_stop":
				if inToolBlock {
					toolCalls = append(toolCalls, ToolCall{
						ID:    currentToolID,
						Name:  currentToolName,
						Input: json.RawMessage(currentToolInput.String()),
					})
					events <- StreamEvent{Type: StreamEventToolCallEnd}
					inToolBlock = false
				}

			case "message_delta":
				if sev.Delta != nil {
					assembled.StopReason = sev.Delta.StopReason
				}
				if sev.Usage != nil {
					assembled.Usage.OutputTokens = sev.Usage.OutputTokens
				}
				events <- StreamEvent{
					Type:       StreamEventUsage,
					Usage:      &assembled.Usage,
					StopReason: assembled.StopReason,
				}

			case "message_stop":
				events <- StreamEvent{Type: StreamEventDone}

			case "error":
				errMsg := "unknown streaming error"
				if sev.Error != nil {
					errMsg = sev.Error.Message
				}
				events <- StreamEvent{
					Type: StreamEventError,
					Err:  fmt.Errorf("anthropic stream: %s", errMsg),
				}
			}

			return nil
		})

		// Assemble final response.
		assembled.Content = textContent.String()
		assembled.ToolCalls = toolCalls

		if parseErr != nil {
			events <- StreamEvent{
				Type: StreamEventError,
				Err:  fmt.Errorf("anthropic stream parse: %w", parseErr),
			}
			sr.SetResponse(nil, parseErr)
		} else {
			sr.SetResponse(&assembled, nil)
		}
	}()

	return sr, nil
}

// buildAnthropicRequest constructs the API request body from a ChatRequest.
// Shared by Chat() and ChatStream().
func (p *AnthropicProvider) buildAnthropicRequest(req ChatRequest) anthropicRequest {
	// Per-request model override takes precedence over the provider's configured model.
	model := req.Model
	if model == "" {
		model = p.config.Model
	}

	apiReq := anthropicRequest{
		Model:     model,
		MaxTokens: req.MaxTokens,
		System:    req.SystemPrompt,
	}

	if apiReq.MaxTokens == 0 {
		apiReq.MaxTokens = 4096
	}
	if apiReq.Model == "" {
		apiReq.Model = "claude-3-5-sonnet-20241022"
	}

	for _, m := range req.Messages {
		if m.Role == "tool" {
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}
			if len(apiReq.Messages) > 0 && apiReq.Messages[len(apiReq.Messages)-1].Role == "user" {
				prev := apiReq.Messages[len(apiReq.Messages)-1]
				switch v := prev.Content.(type) {
				case string:
					apiReq.Messages[len(apiReq.Messages)-1].Content = []any{
						map[string]any{"type": "text", "text": v},
						block,
					}
				case []any:
					apiReq.Messages[len(apiReq.Messages)-1].Content = append(v, block)
				}
			} else {
				apiReq.Messages = append(apiReq.Messages, anthropicMessage{
					Role:    "user",
					Content: []any{block},
				})
			}
		} else if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var content []any
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": json.RawMessage(tc.Input),
				})
			}
			apiReq.Messages = append(apiReq.Messages, anthropicMessage{
				Role:    "assistant",
				Content: content,
			})
		} else {
			apiReq.Messages = append(apiReq.Messages, anthropicMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}
	}

	for _, t := range req.Tools {
		apiReq.Tools = append(apiReq.Tools, anthropicTool(t))
	}

	return apiReq
}
