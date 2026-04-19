package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"daimon/internal/config"
)

// Compile-time interface assertion.
var _ Provider = (*OpenRouterProvider)(nil)

// --------------------------------------------------------------------------
// Wire types — OpenRouter OpenAI-compatible chat completions API
// --------------------------------------------------------------------------

// openrouterMessage is a single chat message in the request.
// Content is `any` (not `omitempty`) so that null assistant messages are encoded
// as JSON null — semantically significant when tool_calls are present.
type openrouterMessage struct {
	Role       string               `json:"role"`
	Content    any                  `json:"content"`
	ToolCalls  []openrouterToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

// openrouterToolCall is used in both request (outbound assistant msgs) and response.
// Arguments is a string because OpenRouter returns it as a JSON-encoded string, not a raw
// object — callers must perform a double-unmarshal to obtain the actual JSON object.
type openrouterToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string — requires double-unmarshal
	} `json:"function"`
}

// openrouterTool is a tool definition in the request.
type openrouterTool struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// openrouterRequest is the full request body sent to /api/v1/chat/completions.
type openrouterRequest struct {
	Model            string              `json:"model"`
	Messages         []openrouterMessage `json:"messages"`
	Tools            []openrouterTool    `json:"tools,omitempty"`
	MaxTokens        int                 `json:"max_tokens,omitempty"`
	IncludeReasoning bool                `json:"include_reasoning,omitempty"` // set when model supports reasoning
}

// openrouterChoice is a single choice in the response.
// Content is *string to distinguish JSON null (tool-call-only response) from "".
type openrouterChoice struct {
	Message struct {
		Role      string               `json:"role"`
		Content   *string              `json:"content"`
		ToolCalls []openrouterToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// openrouterResponse is the top-level API response envelope.
type openrouterResponse struct {
	Choices []openrouterChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// openrouterModelEntry represents a single model from the OpenRouter /api/v1/models endpoint.
type openrouterModelEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

type openrouterModelList struct {
	Data []openrouterModelEntry `json:"data"`
}

// --------------------------------------------------------------------------
// Provider struct and constructor
// --------------------------------------------------------------------------

// ModelInfoStore is an optional interface for looking up cached model metadata.
// Used by OpenRouterProvider to check SupportedParameters before building requests.
// Implemented by the model cache in Phase 5; in tests a fake is injected.
type ModelInfoStore interface {
	GetModelInfo(modelID string) (ModelInfo, bool)
}

// OpenRouterProvider calls the OpenRouter OpenAI-compatible chat completions API.
type OpenRouterProvider struct {
	config         config.ProviderConfig
	baseURL        string
	client         *http.Client
	modelInfoStore ModelInfoStore // optional; nil = no capability checks
}

// SetModelInfoStore wires a model info store into the provider so that
// SupportedParameters can be checked before building each request.
func (p *OpenRouterProvider) SetModelInfoStore(s ModelInfoStore) {
	p.modelInfoStore = s
}

// NewOpenRouterProvider constructs an OpenRouterProvider from cfg.
// No error is returned — config validation is upstream in config.Load().
func NewOpenRouterProvider(cfg config.ProviderConfig) *OpenRouterProvider {
	base := cfg.BaseURL
	if base == "" {
		base = "https://openrouter.ai"
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &OpenRouterProvider{
		config:  cfg,
		baseURL: base,
		client:  &http.Client{Timeout: timeout},
	}
}

// --------------------------------------------------------------------------
// Simple interface methods
// --------------------------------------------------------------------------

func (p *OpenRouterProvider) Name() string             { return "openrouter" }
func (p *OpenRouterProvider) Model() string            { return p.config.Model }
func (p *OpenRouterProvider) SupportsTools() bool      { return true }
func (p *OpenRouterProvider) SupportsMultimodal() bool { return true }
func (p *OpenRouterProvider) SupportsAudio() bool      { return true }

// HealthCheck checks that an API key is configured and returns the model name.
// No HTTP call is made — mirrors AnthropicProvider pattern for startup-latency consistency.
func (p *OpenRouterProvider) HealthCheck(_ context.Context) (string, error) {
	if p.config.APIKey == "" {
		return "", fmt.Errorf("openrouter: missing api_key")
	}
	model := p.config.Model
	if model == "" {
		model = "openrouter/free"
	}
	return model, nil
}

// --------------------------------------------------------------------------
// Chat helpers
// --------------------------------------------------------------------------

// normalizeFinishReason maps OpenAI finish_reason values to the internal StopReason convention.
func normalizeFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}

// mapToolCallsToWire converts internal ToolCall slice to outbound openrouterToolCall wire format.
// Used when encoding assistant messages that contain tool calls in multi-turn conversations.
func mapToolCallsToWire(tcs []ToolCall) []openrouterToolCall {
	out := make([]openrouterToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = openrouterToolCall{
			ID:   tc.ID,
			Type: "function",
		}
		out[i].Function.Name = tc.Name
		out[i].Function.Arguments = string(tc.Input) // json.RawMessage → string (already valid JSON)
	}
	return out
}

// parseToolCalls performs the double-unmarshal for tool call arguments.
// OpenRouter returns Arguments as a JSON-encoded string; we decode it to json.RawMessage.
func parseToolCalls(raw []openrouterToolCall) ([]ToolCall, error) {
	out := make([]ToolCall, 0, len(raw))
	for _, tc := range raw {
		var inputRaw json.RawMessage
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &inputRaw); err != nil {
			return nil, fmt.Errorf("openrouter: parsing tool call arguments for %q: %w", tc.Function.Name, err)
		}
		out = append(out, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: inputRaw,
		})
	}
	return out, nil
}

// parseResponse builds a ChatResponse from the OpenRouter API response.
func (p *OpenRouterProvider) parseResponse(apiResp openrouterResponse) (*ChatResponse, error) {
	if apiResp.Error != nil {
		return nil, fmt.Errorf("openrouter api error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: empty choices in response")
	}

	choice := apiResp.Choices[0]
	out := &ChatResponse{
		StopReason: normalizeFinishReason(choice.FinishReason),
		Usage: UsageStats{
			InputTokens:  apiResp.Usage.PromptTokens,
			OutputTokens: apiResp.Usage.CompletionTokens,
		},
	}

	// Null-coalesce: content is nil when the model returns only tool calls.
	if choice.Message.Content != nil {
		out.Content = *choice.Message.Content
	}

	if len(choice.Message.ToolCalls) > 0 {
		tcs, err := parseToolCalls(choice.Message.ToolCalls)
		if err != nil {
			return nil, err
		}
		out.ToolCalls = tcs
	}

	return out, nil
}

// --------------------------------------------------------------------------
// Chat — main entry point
// --------------------------------------------------------------------------

func (p *OpenRouterProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Step 1: Build messages array.
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
			// Content must be null (not "") when tool_calls are present.
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
		tool.Function.Parameters = t.InputSchema // pass through as-is (no sanitization)
		tools = append(tools, tool)
	}

	// Step 3: Marshal request.
	// Per-request model override takes precedence over the provider's configured model.
	model := req.Model
	if model == "" {
		model = p.config.Model
	}
	apiReq := openrouterRequest{
		Model:    model,
		Messages: msgs,
		Tools:    tools,
	}
	if req.MaxTokens > 0 {
		apiReq.MaxTokens = req.MaxTokens
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshaling request: %w", err)
	}

	// Step 4: Retry loop.
	url := p.baseURL + "/api/v1/chat/completions"
	maxRetries := p.config.MaxRetries

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("openrouter: creating http request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("openrouter: request failed: %w", wrapNetworkError(err))
			if attempt >= maxRetries {
				break
			}
			delay := time.Duration(attempt+1) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var apiResp openrouterResponse
			if err := json.Unmarshal(respBody, &apiResp); err != nil {
				return nil, fmt.Errorf("openrouter: parsing response: %w", err)
			}
			return p.parseResponse(apiResp)
		}

		lastErr = classifyOpenRouterError(resp.StatusCode, respBody)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			if attempt >= maxRetries {
				break
			}
			delay := time.Duration(attempt+1) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue // eligible for retry
		}
		return nil, lastErr // 4xx and other non-retryable errors — immediate return
	}

	return nil, fmt.Errorf("openrouter: failed after %d attempts: %w", maxRetries+1, lastErr)
}

// --------------------------------------------------------------------------
// ListFreeModels — optional exported method for model discovery
// --------------------------------------------------------------------------

// classifyOpenRouterError maps an HTTP status code to a sentinel-wrapped error
// so that isFallbackEligible() works correctly when OpenRouter is the primary provider.
func classifyOpenRouterError(statusCode int, body []byte) error {
	msg := fmt.Sprintf("openrouter api error: %d %s", statusCode, string(body))
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%s: %w", msg, ErrRateLimit)
	case statusCode >= 500:
		return fmt.Errorf("%s: %w", msg, ErrUnavailable)
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("%s: %w", msg, ErrAuth)
	default: // 400 and other 4xx
		return fmt.Errorf("%s: %w", msg, ErrBadRequest)
	}
}

// fetchModels fetches the raw model list from the OpenRouter API.
func (p *OpenRouterProvider) fetchModels(ctx context.Context) ([]openrouterModelEntry, error) {
	url := p.baseURL + "/api/v1/models"

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("openrouter: creating models request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: fetching models: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter: models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var list openrouterModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("openrouter: parsing models response: %w", err)
	}
	return list.Data, nil
}

// parseCostPerMillion converts the OpenRouter per-token price string to USD per 1M tokens.
func parseCostPerMillion(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v * 1_000_000
}

// ListModels returns all models available on OpenRouter with metadata.
// Implements the provider.ModelLister interface.
func (p *OpenRouterProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	entries, err := p.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(entries))
	for _, m := range entries {
		promptCost := parseCostPerMillion(m.Pricing.Prompt)
		completionCost := parseCostPerMillion(m.Pricing.Completion)
		models = append(models, ModelInfo{
			ID:             m.ID,
			Name:           m.Name,
			ContextLength:  m.ContextLength,
			PromptCost:     promptCost,
			CompletionCost: completionCost,
			Free:           m.Pricing.Prompt == "0" && m.Pricing.Completion == "0",
		})
	}
	return models, nil
}

// ListFreeModels returns the IDs of all models on OpenRouter where both prompt and
// completion pricing are "0" (free tier). Not part of the Provider interface.
func (p *OpenRouterProvider) ListFreeModels(ctx context.Context) ([]string, error) {
	entries, err := p.fetchModels(ctx)
	if err != nil {
		return nil, err
	}

	var free []string
	for _, m := range entries {
		if m.Pricing.Prompt == "0" && m.Pricing.Completion == "0" {
			free = append(free, m.ID)
		}
	}
	if free == nil {
		free = []string{}
	}
	return free, nil
}
