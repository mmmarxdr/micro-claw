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
// Gemini SSE wire types — used only for streaming JSON deserialization
// --------------------------------------------------------------------------

// geminiStreamChunk represents a single SSE data payload from the
// streamGenerateContent endpoint.
type geminiStreamChunk struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
		Index        int           `json:"index"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *geminiErrorBody `json:"error,omitempty"`
}

// --------------------------------------------------------------------------
// ChatStream — streaming implementation for Gemini
// --------------------------------------------------------------------------

// ChatStream implements StreamingProvider for Gemini.
// It builds the same request as Chat() but uses the streamGenerateContent
// endpoint with ?alt=sse, then reads SSE events and maps them to StreamEvent values.
func (p *GeminiProvider) ChatStream(ctx context.Context, req ChatRequest) (*StreamResult, error) {
	// 1. Build the same request body as Chat().
	// Per-request model override takes precedence over the provider's configured model.
	model := req.Model
	if model == "" {
		model = p.config.Model
	}
	if model == "" {
		model = "gemini-2.0-flash"
	}

	apiReq := p.buildGeminiRequest(req)

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("gemini stream: marshaling request: %w", err)
	}

	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", baseURL, model, p.config.APIKey)

	// 2. Make HTTP request — use a separate client with NO Timeout for streaming.
	//    Context provides cancellation instead.
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("gemini stream: creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	streamClient := &http.Client{} // no Timeout — context cancellation handles it
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini stream: %w", wrapNetworkError(err))
	}

	// 3. Check HTTP status BEFORE starting to parse SSE.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, classifyGeminiError(resp.StatusCode, body)
	}

	// 4. Launch goroutine to parse SSE and emit StreamEvents.
	sr, events := NewStreamResult(32)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		// Track state for response assembly.
		var textContent strings.Builder
		var toolCalls []ToolCall
		var assembled ChatResponse

		parseErr := ParseSSE(resp.Body, func(ev SSEEvent) error {
			var chunk geminiStreamChunk
			if err := json.Unmarshal(ev.Data, &chunk); err != nil {
				return fmt.Errorf("parsing SSE data: %w", err)
			}

			// Handle error in chunk.
			if chunk.Error != nil {
				events <- StreamEvent{
					Type: StreamEventError,
					Err:  fmt.Errorf("gemini stream: %s", chunk.Error.Message),
				}
				return nil
			}

			// Track usage from every chunk (Gemini sends cumulative usage).
			if chunk.UsageMetadata.PromptTokenCount > 0 || chunk.UsageMetadata.CandidatesTokenCount > 0 {
				assembled.Usage.InputTokens = chunk.UsageMetadata.PromptTokenCount
				assembled.Usage.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
			}

			if len(chunk.Candidates) == 0 {
				return nil
			}

			candidate := chunk.Candidates[0]

			// Process parts.
			for _, part := range candidate.Content.Parts {
				if part.FunctionCall != nil {
					// Gemini sends complete function calls per chunk (not incremental).
					inputBytes, _ := json.Marshal(part.FunctionCall.Args)
					callID := fmt.Sprintf("call_%s", part.FunctionCall.Name)

					toolCalls = append(toolCalls, ToolCall{
						ID:    callID,
						Name:  part.FunctionCall.Name,
						Input: json.RawMessage(inputBytes),
					})

					events <- StreamEvent{
						Type:       StreamEventToolCallStart,
						ToolCallID: callID,
						ToolName:   part.FunctionCall.Name,
					}
					events <- StreamEvent{
						Type:      StreamEventToolCallDelta,
						ToolInput: string(inputBytes),
					}
					events <- StreamEvent{Type: StreamEventToolCallEnd}
				} else if part.Text != "" {
					textContent.WriteString(part.Text)
					events <- StreamEvent{
						Type: StreamEventTextDelta,
						Text: part.Text,
					}
				}
			}

			// Check for finish reason.
			if candidate.FinishReason == "STOP" || candidate.FinishReason == "MAX_TOKENS" {
				assembled.StopReason = normalizeGeminiFinishReason(candidate.FinishReason)

				// Normalise: if we have tool calls, override stop reason.
				if len(toolCalls) > 0 {
					assembled.StopReason = "tool_use"
				}

				events <- StreamEvent{
					Type:       StreamEventUsage,
					Usage:      &assembled.Usage,
					StopReason: assembled.StopReason,
				}
				events <- StreamEvent{Type: StreamEventDone}
			}

			return nil
		})

		// Assemble final response.
		assembled.Content = textContent.String()
		assembled.ToolCalls = toolCalls

		if parseErr != nil {
			events <- StreamEvent{
				Type: StreamEventError,
				Err:  fmt.Errorf("gemini stream parse: %w", parseErr),
			}
			sr.SetResponse(nil, parseErr)
		} else {
			sr.SetResponse(&assembled, nil)
		}
	}()

	return sr, nil
}

// buildGeminiRequest constructs the API request body from a ChatRequest.
// Shared by Chat() and ChatStream().
func (p *GeminiProvider) buildGeminiRequest(req ChatRequest) geminiRequest {
	apiReq := geminiRequest{}

	// System prompt → systemInstruction
	if req.SystemPrompt != "" {
		apiReq.SystemInstruction = &geminiSystemInstruction{
			Parts: []geminiPart{{Text: req.SystemPrompt}},
		}
	}

	// Generation config
	if req.MaxTokens > 0 {
		apiReq.GenerationConfig = &geminiGenerationConfig{MaxOutputTokens: req.MaxTokens}
	}

	// Tools → functionDeclarations
	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			sanitized, err := sanitizeSchemaForGemini(t.InputSchema)
			if err != nil {
				sanitized = t.InputSchema // fall back to original on parse error
			}
			decls = append(decls, geminiFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  sanitized,
			})
		}
		apiReq.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}

	// Messages → contents
	// Gemini uses roles "user" and "model" (not "assistant")
	for _, m := range req.Messages {
		switch m.Role {
		case "tool":
			// Tool result — must be emitted as a "user" turn with functionResponse part
			responseData := map[string]any{"content": m.Content}

			apiReq.Contents = append(apiReq.Contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResp{
						Name:     m.ToolCallID, // we store tool name in ToolCallID
						Response: responseData,
					},
				}},
			})

		case "assistant":
			var parts []geminiPart
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal(tc.Input, &args)
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			if len(parts) > 0 {
				apiReq.Contents = append(apiReq.Contents, geminiContent{Role: "model", Parts: parts})
			}

		default:
			// user
			apiReq.Contents = append(apiReq.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		}
	}

	return apiReq
}
