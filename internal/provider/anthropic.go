package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"microagent/internal/config"
)

type AnthropicProvider struct {
	config config.ProviderConfig
	client *http.Client
}

func NewAnthropicProvider(cfg config.ProviderConfig) *AnthropicProvider {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &AnthropicProvider{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) SupportsTools() bool {
	return true
}

func (p *AnthropicProvider) HealthCheck(ctx context.Context) (string, error) {
	if p.config.APIKey == "" {
		return "", fmt.Errorf("anthropic: missing api_key")
	}
	model := p.config.Model
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	return model, nil
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	Type       string `json:"type"`
	Role       string `json:"role"`
	Content    []any  `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	apiReq := anthropicRequest{
		Model:     p.config.Model,
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

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	url := "https://api.anthropic.com/v1/messages"
	if p.config.BaseURL != "" {
		url = p.config.BaseURL
	}

	var lastErr error
	maxRetries := p.config.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("creating http request: %w", err)
		}

		httpReq.Header.Set("x-api-key", p.config.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("content-type", "application/json")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("anthropic: request failed: %w", wrapNetworkError(err))
			delay := time.Duration(attempt+1) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = classifyAnthropicError(resp.StatusCode, respBody)
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				delay := time.Duration(attempt+1) * 2 * time.Second
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				continue
			}
			return nil, lastErr
		}

		var apiResp anthropicResponse
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, fmt.Errorf("parsing anthropic response: %w", err)
		}

		return p.parseResponse(apiResp)
	}

	return nil, fmt.Errorf("failed after %d attempts, last error: %w", maxRetries, lastErr)
}

// classifyAnthropicError maps HTTP status codes to sentinel errors.
func classifyAnthropicError(statusCode int, body []byte) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: anthropic api error %d %s", ErrRateLimit, statusCode, string(body))
	case statusCode >= 500:
		return fmt.Errorf("%w: anthropic api error %d %s", ErrUnavailable, statusCode, string(body))
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("%w: anthropic api error %d %s", ErrAuth, statusCode, string(body))
	default: // 400 and other 4xx
		return fmt.Errorf("%w: anthropic api error %d %s", ErrBadRequest, statusCode, string(body))
	}
}

func (p *AnthropicProvider) parseResponse(apiResp anthropicResponse) (*ChatResponse, error) {
	if apiResp.Error != nil {
		return nil, fmt.Errorf("api error: %s", apiResp.Error.Message)
	}

	out := &ChatResponse{
		StopReason: apiResp.StopReason,
		Usage: UsageStats{
			InputTokens:  apiResp.Usage.InputTokens,
			OutputTokens: apiResp.Usage.OutputTokens,
		},
	}

	for _, block := range apiResp.Content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		switch blockMap["type"].(string) {
		case "text":
			if text, ok := blockMap["text"].(string); ok {
				if out.Content != "" {
					out.Content += "\n"
				}
				out.Content += text
			}
		case "tool_use":
			id, _ := blockMap["id"].(string)
			name, _ := blockMap["name"].(string)
			inputMap, _ := blockMap["input"].(map[string]any)
			inputBytes, _ := json.Marshal(inputMap)

			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:    id,
				Name:  name,
				Input: json.RawMessage(inputBytes),
			})
		}
	}

	return out, nil
}
