package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"daimon/internal/config"
)

// --------------------------------------------------------------------------
// Thinking capability map (Phase 2.1)
// --------------------------------------------------------------------------

// thinkingShape describes the kind of extended thinking a model supports.
type thinkingShape int

const (
	// thinkingNone means the model does not support extended thinking.
	// Sending thinking params to such models causes a 400 error.
	thinkingNone thinkingShape = iota
	// thinkingAdaptive means the model uses {"type":"adaptive","effort":"..."}.
	// Currently only claude-opus-4-7 uses this shape.
	thinkingAdaptive
	// thinkingManual means the model uses {"type":"enabled","budget_tokens":N}.
	// Used by 4.6/4.5/4.1 model generations.
	thinkingManual
)

// anthropicThinkingCapability maps model IDs to their thinking shape.
// Absence from the map implies thinkingNone (safe default — no thinking params sent).
var anthropicThinkingCapability = map[string]thinkingShape{
	// claude-opus-4-7: adaptive shape (REQUIRES adaptive — rejects "enabled")
	"claude-opus-4-7": thinkingAdaptive,
	// claude-opus-4-6 and claude-sonnet-4-6: manual shape
	"claude-opus-4-6":   thinkingManual,
	"claude-sonnet-4-6": thinkingManual,
	// Legacy IDs — manual shape
	"claude-opus-4-5-20251101":   thinkingManual,
	"claude-sonnet-4-5-20250929": thinkingManual,
	"claude-opus-4-1-20250805":   thinkingManual,
	// claude-haiku-4-5-20251001 is intentionally absent → thinkingNone
}

// anthropicThinkingParams returns the thinking activation map[string]any to inject
// into the request body, or nil if the model does not support thinking.
// effort: "low" | "medium" | "high" (used for adaptive shape)
// budgetTokens: integer token budget (used for manual shape)
func anthropicThinkingParams(modelID, effort string, budgetTokens int) map[string]any {
	shape := anthropicThinkingCapability[modelID]
	switch shape {
	case thinkingAdaptive:
		return map[string]any{
			"type":   "adaptive",
			"effort": effort,
		}
	case thinkingManual:
		return map[string]any{
			"type":         "enabled",
			"budget_tokens": budgetTokens,
		}
	default:
		return nil
	}
}

// --------------------------------------------------------------------------
// AnthropicProvider
// --------------------------------------------------------------------------

type AnthropicProvider struct {
	config   config.ProviderConfig
	client   *http.Client
	media    mediaReader // optional; nil → text-only fallback for image blocks
	thinking config.ProviderCredentials // Anthropic-specific thinking config
}

// SetThinkingConfig wires Anthropic-specific thinking settings (effort, budget)
// from the ProviderCredentials into the provider. Called after construction
// when config is available (e.g. in buildAnthropicRequest).
func (p *AnthropicProvider) SetThinkingConfig(creds config.ProviderCredentials) {
	p.thinking = creds
}

func NewAnthropicProvider(cfg config.ProviderConfig) *AnthropicProvider {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &AnthropicProvider{
		config:   cfg,
		client:   &http.Client{Timeout: timeout},
	}
}

// WithMediaReader wires a mediaReader into the provider so that image blocks
// can be translated to base64 Anthropic content parts. Callers that do not yet
// have a store (e.g. text-only test fixtures) leave this unset; the provider
// falls back gracefully to placeholder text for any image blocks it encounters.
//
// Phase 4's *store.SQLiteStore will satisfy this interface automatically.
func (p *AnthropicProvider) WithMediaReader(mr mediaReader) *AnthropicProvider {
	p.media = mr
	return p
}

func (p *AnthropicProvider) Name() string              { return "anthropic" }
func (p *AnthropicProvider) Model() string             { return p.config.Model }
func (p *AnthropicProvider) SupportsTools() bool       { return true }
func (p *AnthropicProvider) SupportsMultimodal() bool { return true }
func (p *AnthropicProvider) SupportsAudio() bool      { return false }

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
	Thinking  map[string]any     `json:"thinking,omitempty"` // extended thinking params; nil = omitted
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
	apiReq := p.buildAnthropicRequest(ctx, req)

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

// ListModels fetches the list of available models from the Anthropic API.
// Implements the provider.ModelLister interface.
func (p *AnthropicProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := "https://api.anthropic.com/v1/models?limit=100"
	if p.config.BaseURL != "" {
		url = p.config.BaseURL + "/v1/models?limit=100"
	}

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic: creating models request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.config.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: fetching models: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic: models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("anthropic: parsing models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{
			ID:   m.ID,
			Name: m.DisplayName,
		})
	}
	return models, nil
}
