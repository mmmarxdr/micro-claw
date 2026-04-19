package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"daimon/internal/config"
	"daimon/internal/content"
)

// Compile-time interface assertion.
var _ Provider = (*OpenAIProvider)(nil)

// --------------------------------------------------------------------------
// Wire types — OpenAI Chat Completions API (also compatible with Ollama)
// --------------------------------------------------------------------------

// openaiMessage is a single chat message in the request.
// Content is `any` (not `omitempty`) so that null assistant messages are encoded
// as JSON null — semantically significant when tool_calls are present.
type openaiMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openaiToolCall is used in both request (outbound assistant msgs) and response.
// Arguments is a string because OpenAI returns it as a JSON-encoded string, not a raw
// object — callers must perform a double-unmarshal to obtain the actual JSON object.
type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded string — requires double-unmarshal
	} `json:"function"`
}

// openaiTool is a tool definition in the request.
type openaiTool struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// openaiRequest is the full request body sent to /chat/completions.
type openaiRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Tools     []openaiTool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

// openaiChoice is a single choice in the response.
// Content is *string to distinguish JSON null (tool-call-only response) from "".
type openaiChoice struct {
	Message struct {
		Role      string           `json:"role"`
		Content   *string          `json:"content"`
		ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// openaiResponse is the top-level API response envelope.
type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code,omitempty"` // can be string or int
	} `json:"error,omitempty"`
}

// --------------------------------------------------------------------------
// Provider struct and constructor
// --------------------------------------------------------------------------

const (
	openAIDefaultBaseURL = "https://api.openai.com/v1"
	openAIDefaultModel   = "gpt-4o"
)

// OpenAIProvider calls the OpenAI Chat Completions API (or any compatible API
// such as Ollama via a custom base_url).
type OpenAIProvider struct {
	baseURL    string
	apiKey     string
	model      string
	timeout    time.Duration
	maxRetries int
	client     *http.Client
	media      mediaReader // optional; nil → text-only fallback for image blocks
}

// WithMediaReader wires a mediaReader into the provider so that image blocks
// can be translated to base64 OpenAI content parts. Callers that do not yet
// have a store (e.g. text-only test fixtures) leave this unset; the provider
// falls back gracefully to placeholder text for any image blocks it encounters.
func (p *OpenAIProvider) WithMediaReader(mr mediaReader) *OpenAIProvider {
	p.media = mr
	return p
}

// NewOpenAIProvider constructs an OpenAIProvider from cfg.
// Returns an error if the base URL points to OpenAI but no api_key is set.
func NewOpenAIProvider(cfg config.ProviderConfig) (*OpenAIProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = openAIDefaultBaseURL
	}

	// Require api_key when targeting OpenAI directly.
	if baseURL == openAIDefaultBaseURL && cfg.APIKey == "" {
		return nil, fmt.Errorf("openai: api_key is required when using the OpenAI API")
	}

	model := cfg.Model
	if model == "" {
		model = openAIDefaultModel
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	return &OpenAIProvider{
		baseURL:    baseURL,
		apiKey:     cfg.APIKey,
		model:      model,
		timeout:    timeout,
		maxRetries: cfg.MaxRetries,
		client:     &http.Client{Timeout: timeout},
	}, nil
}

// --------------------------------------------------------------------------
// Simple interface methods
// --------------------------------------------------------------------------

func (p *OpenAIProvider) Name() string             { return "openai" }
func (p *OpenAIProvider) Model() string            { return p.model }
func (p *OpenAIProvider) SupportsTools() bool      { return true }
func (p *OpenAIProvider) SupportsMultimodal() bool { return true }
func (p *OpenAIProvider) SupportsAudio() bool      { return true }

// HealthCheck verifies configuration and returns the model name.
// No HTTP call is made — mirrors the AnthropicProvider pattern for startup-latency consistency.
func (p *OpenAIProvider) HealthCheck(_ context.Context) (string, error) {
	if p.baseURL == openAIDefaultBaseURL && p.apiKey == "" {
		return "", fmt.Errorf("openai: missing api_key")
	}
	return p.model, nil
}

// --------------------------------------------------------------------------
// Multimodal translation helpers
// --------------------------------------------------------------------------

// translateBlocks converts a content.Blocks slice into the OpenAI chat completions
// content parts format.
//
//   - BlockText  → {"type":"text","text":"..."}
//   - BlockImage → {"type":"image_url","image_url":{"url":"data:<mime>;base64,<b64>"}}
//     Bytes are fetched from p.media (mediaReader). If p.media is nil, or GetMedia
//     returns an error, a text placeholder is substituted and a warning is logged.
//   - BlockAudio / BlockDocument → OpenAI chat completions does not natively support
//     these via base64 inline data; fall back to FlattenBlocks placeholder text.
//
// Returns nil when bs is empty (caller should emit a plain-string content field).
func (p *OpenAIProvider) translateBlocks(ctx context.Context, bs content.Blocks) []any {
	if len(bs) == 0 {
		return nil
	}

	parts := make([]any, 0, len(bs))
	for _, b := range bs {
		switch b.Type {
		case content.BlockText:
			parts = append(parts, map[string]any{
				"type": "text",
				"text": b.Text,
			})

		case content.BlockImage:
			imgBytes, mime, err := p.fetchMedia(ctx, b)
			if err != nil {
				// Graceful degradation: log and substitute placeholder text.
				slog.Warn("openai: failed to load media, substituting placeholder",
					"sha256", b.MediaSHA256,
					"err", err,
				)
				parts = append(parts, map[string]any{
					"type": "text",
					"text": fmt.Sprintf("[image unavailable: %s]", b.MediaSHA256),
				})
				continue
			}
			dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(imgBytes)
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": dataURL,
				},
			})

		default:
			// BlockAudio, BlockDocument, and future types: fall back to FlattenBlocks
			// placeholder. OpenAI chat completions does not support inline audio.
			placeholder := content.FlattenBlocks(content.Blocks{b})
			parts = append(parts, map[string]any{
				"type": "text",
				"text": placeholder,
			})
		}
	}
	return parts
}

// fetchMedia loads bytes for a media block from p.media.
// Returns an error if p.media is nil or GetMedia fails.
func (p *OpenAIProvider) fetchMedia(ctx context.Context, b content.ContentBlock) ([]byte, string, error) {
	if p.media == nil {
		return nil, "", fmt.Errorf("no media reader configured")
	}
	imgBytes, mime, err := p.media.GetMedia(ctx, b.MediaSHA256)
	if err != nil {
		return nil, "", err
	}
	// Prefer the MIME from the store response; fall back to block's MIME field.
	if mime == "" {
		mime = b.MIME
	}
	return imgBytes, mime, nil
}

// --------------------------------------------------------------------------
// Chat helpers
// --------------------------------------------------------------------------

// mapOpenAIToolCallsToWire converts internal ToolCall slice to the outbound openaiToolCall wire format.
// Used when encoding assistant messages that contain tool calls in multi-turn conversations.
func mapOpenAIToolCallsToWire(tcs []ToolCall) []openaiToolCall {
	out := make([]openaiToolCall, len(tcs))
	for i, tc := range tcs {
		out[i] = openaiToolCall{
			ID:   tc.ID,
			Type: "function",
		}
		out[i].Function.Name = tc.Name
		out[i].Function.Arguments = string(tc.Input) // json.RawMessage → string (already valid JSON)
	}
	return out
}

// parseOpenAIToolCalls performs the double-unmarshal for tool call arguments.
// OpenAI returns Arguments as a JSON-encoded string; we decode it to json.RawMessage.
func parseOpenAIToolCalls(raw []openaiToolCall) ([]ToolCall, error) {
	out := make([]ToolCall, 0, len(raw))
	for _, tc := range raw {
		var inputRaw json.RawMessage
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &inputRaw); err != nil {
			return nil, fmt.Errorf("openai: parsing tool call arguments for %q: %w", tc.Function.Name, err)
		}
		out = append(out, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: inputRaw,
		})
	}
	return out, nil
}

// parseOpenAIResponse builds a ChatResponse from the OpenAI API response.
func (p *OpenAIProvider) parseOpenAIResponse(apiResp openaiResponse) (*ChatResponse, error) {
	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai api error: %v", apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices in response")
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
		tcs, err := parseOpenAIToolCalls(choice.Message.ToolCalls)
		if err != nil {
			return nil, err
		}
		out.ToolCalls = tcs
	}

	return out, nil
}

// classifyOpenAIError maps an HTTP status code to a sentinel-wrapped error.
func classifyOpenAIError(statusCode int, body []byte) error {
	msg := fmt.Sprintf("openai api error: %d %s", statusCode, string(body))
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

// --------------------------------------------------------------------------
// Chat — main entry point
// --------------------------------------------------------------------------

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Step 1: Build messages array.
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
			// Content must be null (not "") when tool_calls are present.
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
		tool.Function.Parameters = t.InputSchema // pass through as-is (no sanitization)
		tools = append(tools, tool)
	}

	// Step 3: Marshal request.
	// Per-request model override takes precedence over the provider's configured model.
	model := req.Model
	if model == "" {
		model = p.model
	}
	apiReq := openaiRequest{
		Model:    model,
		Messages: msgs,
		Tools:    tools,
	}
	if req.MaxTokens > 0 {
		apiReq.MaxTokens = req.MaxTokens
	}

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshaling request: %w", err)
	}

	// Step 4: Retry loop.
	url := p.baseURL + "/chat/completions"
	maxRetries := p.maxRetries

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("openai: creating http request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("openai: request failed: %w", wrapNetworkError(err))
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
			var apiResp openaiResponse
			if err := json.Unmarshal(respBody, &apiResp); err != nil {
				return nil, fmt.Errorf("openai: parsing response: %w", err)
			}
			return p.parseOpenAIResponse(apiResp)
		}

		lastErr = classifyOpenAIError(resp.StatusCode, respBody)
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

	return nil, fmt.Errorf("openai: failed after %d attempts: %w", maxRetries+1, lastErr)
}

// ─── EmbeddingProvider implementation ────────────────────────────────────────

// openaiEmbedRequest is the request body for /v1/embeddings.
type openaiEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// openaiEmbedResponse is the API response for /v1/embeddings.
type openaiEmbedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed generates a text embedding via the OpenAI embeddings API using
// text-embedding-3-small. Implements EmbeddingProvider.
// The returned vector length reflects the model output; callers should
// normalize to the expected storage dimension (256) before persisting.
func (p *OpenAIProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody := openaiEmbedRequest{
		Model: "text-embedding-3-small",
		Input: text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshaling request: %w", err)
	}

	url := p.baseURL + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openai embed: creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai embed: request failed: %w", wrapNetworkError(err))
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyOpenAIError(resp.StatusCode, respBody)
	}

	var apiResp openaiEmbedResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("openai embed: parsing response: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("openai embed api error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Data) == 0 {
		return nil, fmt.Errorf("openai embed: empty data in response")
	}

	raw := apiResp.Data[0].Embedding
	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}
	return vec, nil
}

// compile-time check: OpenAIProvider implements EmbeddingProvider.
var _ EmbeddingProvider = (*OpenAIProvider)(nil)

// ListModels fetches the list of available models from the OpenAI-compatible API.
// Implements the provider.ModelLister interface.
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := p.baseURL + "/models"

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("openai: creating models request: %w", err)
	}
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: fetching models: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("openai: parsing models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, ModelInfo{
			ID:   m.ID,
			Name: m.ID,
		})
	}
	return models, nil
}
