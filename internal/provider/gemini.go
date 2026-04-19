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

// GeminiProvider implements the Provider interface via the Google AI Studio REST API
// (generateContent endpoint). Works with any model: gemini-2.0-flash, gemini-1.5-pro, etc.
type GeminiProvider struct {
	config config.ProviderConfig
	client *http.Client
	media  mediaReader // optional; nil → text-only fallback for image/audio blocks
}

func NewGeminiProvider(cfg config.ProviderConfig) *GeminiProvider {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &GeminiProvider{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}
}

// WithMediaReader wires a mediaReader into the provider so that image and audio
// blocks can be translated to base64 Gemini inlineData parts. Callers that do
// not yet have a store (e.g. text-only test fixtures) leave this unset; the
// provider falls back gracefully to placeholder text for any media blocks.
func (p *GeminiProvider) WithMediaReader(mr mediaReader) *GeminiProvider {
	p.media = mr
	return p
}

func (p *GeminiProvider) Name() string             { return "gemini" }
func (p *GeminiProvider) Model() string            { return p.config.Model }
func (p *GeminiProvider) SupportsTools() bool      { return true }
func (p *GeminiProvider) SupportsMultimodal() bool { return true }
func (p *GeminiProvider) SupportsAudio() bool      { return true }

// --------------------------------------------------------------------------
// Wire types — Gemini generateContent REST API
// --------------------------------------------------------------------------

type geminiPart struct {
	Text             string              `json:"text,omitempty"`
	InlineData       *geminiInlineData   `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResp `json:"functionResponse,omitempty"`
}

// geminiInlineData carries base64-encoded media for image and audio blocks.
type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"` // base64-encoded bytes
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFunctionResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	Contents          []geminiContent          `json:"contents"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *geminiErrorBody `json:"error,omitempty"`
}

type geminiErrorBody struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Status  string         `json:"status"`
	Details []geminiDetail `json:"details"`
}

type geminiDetail struct {
	Type       string `json:"@type"`
	RetryDelay string `json:"retryDelay,omitempty"` // e.g. "35s"
}

// geminiErrorResponse is the top-level envelope for error-only responses.
type geminiErrorResponse struct {
	Error *geminiErrorBody `json:"error"`
}

// --------------------------------------------------------------------------
// Chat — main entry point
// --------------------------------------------------------------------------

func (p *GeminiProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Per-request model override takes precedence over the provider's configured model.
	model := req.Model
	if model == "" {
		model = p.config.Model
	}
	if model == "" {
		model = "gemini-2.0-flash"
	}

	apiReq := p.buildGeminiRequest(ctx, req)

	bodyBytes, err := json.Marshal(apiReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshaling request: %w", err)
	}

	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", baseURL, model, p.config.APIKey)

	maxRetries := p.config.MaxRetries
	if maxRetries < 1 {
		maxRetries = 1
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("gemini: creating request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("gemini: request failed: %w", wrapNetworkError(err))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = classifyGeminiError(resp.StatusCode, respBody)
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				// Honour the server-requested retry delay if present.
				delay := retryDelayFromGeminiBody(respBody, time.Duration(attempt+1)*2*time.Second)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				continue
			}
			return nil, lastErr
		}

		var apiResp geminiResponse
		if err := json.Unmarshal(respBody, &apiResp); err != nil {
			return nil, fmt.Errorf("gemini: parsing response: %w", err)
		}

		return p.parseResponse(apiResp)
	}

	return nil, fmt.Errorf("gemini: failed after %d attempts: %w", maxRetries, lastErr)
}

// HealthCheck calls GET /v1beta/models/{model} — a read-only metadata endpoint
// that does NOT consume any generation quota. Returns model display name on success.
func (p *GeminiProvider) HealthCheck(ctx context.Context) (string, error) {
	model := p.config.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}
	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s?key=%s", baseURL, model, p.config.APIKey)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("gemini health check: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("gemini health check: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini health check failed (%d): %s", resp.StatusCode, string(body))
	}

	var info struct {
		DisplayName string `json:"displayName"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("gemini health check: parse error: %w", err)
	}
	if info.DisplayName == "" {
		info.DisplayName = info.Name
	}
	return info.DisplayName, nil
}

// classifyGeminiError maps HTTP status codes to sentinel errors.
func classifyGeminiError(statusCode int, body []byte) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: gemini api error %d %s", ErrRateLimit, statusCode, string(body))
	case statusCode >= 500:
		return fmt.Errorf("%w: gemini api error %d %s", ErrUnavailable, statusCode, string(body))
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return fmt.Errorf("%w: gemini api error %d %s", ErrAuth, statusCode, string(body))
	default: // 400 and other 4xx
		return fmt.Errorf("%w: gemini api error %d %s", ErrBadRequest, statusCode, string(body))
	}
}

// retryDelayFromGeminiBody parses the retryDelay from a Gemini error body.
// Gemini includes it as a RetryInfo detail, e.g. {"retryDelay": "35s"}.
// Falls back to defaultDelay if parsing fails or no delay is present.
func retryDelayFromGeminiBody(body []byte, defaultDelay time.Duration) time.Duration {
	var errResp geminiErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil || errResp.Error == nil {
		return defaultDelay
	}
	for _, d := range errResp.Error.Details {
		if d.RetryDelay == "" {
			continue
		}
		if dur, err := time.ParseDuration(d.RetryDelay); err == nil && dur > 0 {
			return dur
		}
	}
	return defaultDelay
}

// --------------------------------------------------------------------------
// Response parsing
// --------------------------------------------------------------------------

func (p *GeminiProvider) parseResponse(apiResp geminiResponse) (*ChatResponse, error) {
	if apiResp.Error != nil {
		return nil, fmt.Errorf("gemini api error %d: %s", apiResp.Error.Code, apiResp.Error.Message)
	}
	if len(apiResp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: empty candidates in response")
	}

	candidate := apiResp.Candidates[0]

	out := &ChatResponse{
		StopReason: normalizeGeminiFinishReason(candidate.FinishReason),
		Usage: UsageStats{
			InputTokens:  apiResp.UsageMetadata.PromptTokenCount,
			OutputTokens: apiResp.UsageMetadata.CandidatesTokenCount,
		},
	}

	for _, part := range candidate.Content.Parts {
		if part.FunctionCall != nil {
			inputBytes, _ := json.Marshal(part.FunctionCall.Args)
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				// Gemini doesn't issue an ID for function calls — synthesise one from the name.
				ID:    fmt.Sprintf("call_%s", part.FunctionCall.Name),
				Name:  part.FunctionCall.Name,
				Input: json.RawMessage(inputBytes),
			})
		} else if part.Text != "" {
			if out.Content != "" {
				out.Content += "\n"
			}
			out.Content += part.Text
		}
	}

	// Gemini signals tool calls via STOP reason but attaches functionCall parts — normalise.
	if len(out.ToolCalls) > 0 {
		out.StopReason = "tool_use"
	}

	return out, nil
}

// normalizeGeminiFinishReason converts Gemini's finish reason to the internal convention.
func normalizeGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "end_turn"
	case "MAX_TOKENS":
		return "max_tokens"
	default:
		return reason
	}
}

// ─── EmbeddingProvider implementation ────────────────────────────────────────

// geminiEmbedRequest is the request body for the embedContent endpoint.
type geminiEmbedRequest struct {
	Model   string          `json:"model"`
	Content geminiEmbedPart `json:"content"`
}

type geminiEmbedPart struct {
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

// geminiEmbedResponse is the response from the embedContent endpoint.
type geminiEmbedResponse struct {
	Embedding struct {
		Values []float64 `json:"values"`
	} `json:"embedding"`
	Error *geminiErrorBody `json:"error,omitempty"`
}

// Embed generates a text embedding via the Gemini embedding API using
// text-embedding-004. Implements EmbeddingProvider.
// The returned vector length reflects the model output; callers should
// normalize to the expected storage dimension (256) before persisting.
func (p *GeminiProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	model := "text-embedding-004"

	reqBody := geminiEmbedRequest{
		Model: "models/" + model,
		Content: geminiEmbedPart{
			Parts: []struct {
				Text string `json:"text"`
			}{{Text: text}},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: marshaling request: %w", err)
	}

	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:embedContent?key=%s", baseURL, model, p.config.APIKey)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("gemini embed: creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini embed: request failed: %w", wrapNetworkError(err))
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, classifyGeminiError(resp.StatusCode, respBody)
	}

	var apiResp geminiEmbedResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("gemini embed: parsing response: %w", err)
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("gemini embed api error %d: %s", apiResp.Error.Code, apiResp.Error.Message)
	}
	if len(apiResp.Embedding.Values) == 0 {
		return nil, fmt.Errorf("gemini embed: empty values in response")
	}

	raw := apiResp.Embedding.Values
	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}
	return vec, nil
}

// compile-time check: GeminiProvider implements EmbeddingProvider.
var _ EmbeddingProvider = (*GeminiProvider)(nil)

// geminiAllowedSchemaKeys is the documented subset of JSON Schema keywords Gemini accepts
// in function declarations. Everything else is stripped to avoid 400 errors.
// Reference: https://ai.google.dev/gemini-api/docs/function-calling#function-declarations
var geminiAllowedSchemaKeys = map[string]bool{
	"type":        true,
	"description": true,
	"properties":  true,
	"required":    true,
	"enum":        true,
	"items":       true, // for array types
	"nullable":    true, // Gemini extension
}

// sanitizeSchemaForGemini walks a JSON Schema object and removes any keys that
// are not in the Gemini-supported subset. It operates recursively on
// nested objects (properties values and array items).
func sanitizeSchemaForGemini(schema json.RawMessage) (json.RawMessage, error) {
	if len(schema) == 0 {
		return schema, nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(schema, &obj); err != nil {
		// Not a JSON object (e.g. a scalar) — return as-is.
		return schema, nil
	}

	clean := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		if !geminiAllowedSchemaKeys[k] {
			continue // drop unsupported key
		}

		switch k {
		case "properties":
			// Recursively sanitize each property sub-schema.
			var props map[string]json.RawMessage
			if err := json.Unmarshal(v, &props); err == nil {
				sanitizedProps := make(map[string]json.RawMessage, len(props))
				for pk, pv := range props {
					san, err := sanitizeSchemaForGemini(pv)
					if err != nil {
						san = pv
					}
					sanitizedProps[pk] = san
				}
				re, err := json.Marshal(sanitizedProps)
				if err == nil {
					v = re
				}
			}
			clean[k] = v
		case "items":
			// Recursively sanitize array item schema.
			san, err := sanitizeSchemaForGemini(v)
			if err != nil {
				san = v
			}
			clean[k] = san
		default:
			clean[k] = v
		}
	}

	return json.Marshal(clean)
}

// ListModels fetches the list of available models from the Gemini API.
// Implements the provider.ModelLister interface.
func (p *GeminiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := baseURL + "/v1beta/models?key=" + p.config.APIKey

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("gemini: creating models request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: fetching models: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Models []struct {
			Name                       string `json:"name"`
			DisplayName                string `json:"displayName"`
			InputTokenLimit            int    `json:"inputTokenLimit"`
			OutputTokenLimit           int    `json:"outputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("gemini: parsing models response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		// Only include models that support generateContent.
		supportsChat := false
		for _, method := range m.SupportedGenerationMethods {
			if method == "generateContent" {
				supportsChat = true
				break
			}
		}
		if !supportsChat {
			continue
		}
		// Strip "models/" prefix from name.
		id := m.Name
		if len(id) > 7 && id[:7] == "models/" {
			id = id[7:]
		}
		models = append(models, ModelInfo{
			ID:            id,
			Name:          m.DisplayName,
			ContextLength: m.InputTokenLimit,
		})
	}
	return models, nil
}
