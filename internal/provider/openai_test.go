package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"microagent/internal/config"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func newOpenAITestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, config.ProviderConfig) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.ProviderConfig{
		Type:    "openai",
		Model:   "gpt-4o",
		APIKey:  "test-key",
		BaseURL: srv.URL,
	}
	return srv, cfg
}

func openAITextResponse(content string) openaiResponse {
	c := content
	return openaiResponse{
		Choices: []openaiChoice{
			{
				Message: struct {
					Role      string           `json:"role"`
					Content   *string          `json:"content"`
					ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
				}{
					Role:    "assistant",
					Content: &c,
				},
				FinishReason: "stop",
			},
		},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}{PromptTokens: 10, CompletionTokens: 5},
	}
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestNewOpenAIProvider_RequiresAPIKeyForOpenAI(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   "openai",
		Model:  "gpt-4o",
		APIKey: "", // no key
		// BaseURL not set → defaults to openAIDefaultBaseURL
	}
	_, err := NewOpenAIProvider(cfg)
	if err == nil {
		t.Fatal("expected error when api_key is empty for OpenAI default endpoint")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("expected error mentioning api_key, got: %v", err)
	}
}

func TestNewOpenAIProvider_OllamaNoAPIKeyRequired(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:    "openai",
		Model:   "llama3.2",
		APIKey:  "", // no key — Ollama doesn't need one
		BaseURL: "http://localhost:11434/v1",
	}
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("expected no error for Ollama config, got: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("expected name 'openai', got %q", p.Name())
	}
	if p.Model() != "llama3.2" {
		t.Errorf("expected model 'llama3.2', got %q", p.Model())
	}
}

func TestOpenAIProvider_ChatTextResponse(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("expected 'Bearer test-key', got %q", auth)
		}

		w.Header().Set("Content-Type", "application/json")
		resp := openAITextResponse("Hello from OpenAI!")
		_ = json.NewEncoder(w).Encode(resp)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Hello from OpenAI!" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("unexpected stop reason: %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("unexpected input tokens: %d", resp.Usage.InputTokens)
	}
}

func TestOpenAIProvider_ChatWithToolCalls(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		toolResp := openaiResponse{
			Choices: []openaiChoice{
				{
					Message: struct {
						Role      string           `json:"role"`
						Content   *string          `json:"content"`
						ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
					}{
						Role:    "assistant",
						Content: nil,
						ToolCalls: []openaiToolCall{
							{
								ID:   "call_abc",
								Type: "function",
								Function: struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								}{
									Name:      "shell_exec",
									Arguments: `{"command":"ls"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(toolResp)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Run ls"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("unexpected tool call ID: %q", tc.ID)
	}
	if tc.Name != "shell_exec" {
		t.Errorf("unexpected tool name: %q", tc.Name)
	}
	if string(tc.Input) != `{"command":"ls"}` {
		t.Errorf("unexpected tool input: %s", tc.Input)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("unexpected stop reason: %q", resp.StopReason)
	}
}

func TestOpenAIProvider_ChatError401(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	_, err = p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !isErr(err, ErrAuth) {
		t.Errorf("expected ErrAuth, got: %v", err)
	}
}

func TestOpenAIProvider_ChatError500(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"internal server error"}}`, http.StatusInternalServerError)
	})
	cfg.MaxRetries = 0 // don't retry in this test

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	_, err = p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !isErr(err, ErrUnavailable) {
		t.Errorf("expected ErrUnavailable, got: %v", err)
	}
}

func TestOpenAIProvider_ChatRetryOn429(t *testing.T) {
	var callCount int32

	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			http.Error(w, `{"error":{"message":"rate limit"}}`, http.StatusTooManyRequests)
			return
		}
		// Second call succeeds.
		w.Header().Set("Content-Type", "application/json")
		resp := openAITextResponse("OK after retry")
		_ = json.NewEncoder(w).Encode(resp)
	})
	cfg.MaxRetries = 2

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "OK after retry" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", callCount)
	}
}

func TestOpenAIProvider_OllamaMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ollama: no Authorization header required.
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Logf("note: Authorization header was set to %q (may be OK with api_key=ollama)", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := openAITextResponse("Ollama response")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := config.ProviderConfig{
		Type:    "openai",
		Model:   "llama3.2",
		APIKey:  "", // empty — Ollama mode
		BaseURL: srv.URL,
	}

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Ollama response" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestOpenAIProvider_HealthCheck(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:    "openai",
		Model:   "gpt-4o",
		APIKey:  "test-key",
		BaseURL: "http://localhost:99999", // unreachable — HealthCheck makes no HTTP call
	}
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	name, err := p.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if name != "gpt-4o" {
		t.Errorf("expected model name 'gpt-4o', got %q", name)
	}
}

func TestOpenAIProvider_SupportsTools(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:    "openai",
		Model:   "gpt-4o",
		APIKey:  "test-key",
		BaseURL: "http://localhost:99999",
	}
	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if !p.SupportsTools() {
		t.Error("expected SupportsTools() == true")
	}
}

// ─── Embed tests ─────────────────────────────────────────────────────────────

func TestOpenAIProvider_Embed_HappyPath(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/embeddings") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify request body contains correct model and dimensions.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if body["model"] != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %v", body["model"])
		}

		// Return a synthetic 256-dim embedding.
		embedding := make([]float64, 256)
		for i := range embedding {
			embedding[i] = float64(i) * 0.001
		}
		resp := map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "index": 0, "embedding": embedding},
			},
			"usage": map[string]any{"prompt_tokens": 5, "total_tokens": 5},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	vec, err := p.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 256 {
		t.Errorf("expected 256 dims, got %d", len(vec))
	}
	// Verify a few values.
	if vec[0] != 0.0 {
		t.Errorf("vec[0] = %v, want 0.0", vec[0])
	}
}

func TestOpenAIProvider_Embed_Error401(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	_, err = p.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error from 401 response")
	}
	if !isErr(err, ErrAuth) {
		t.Errorf("expected ErrAuth, got: %v", err)
	}
}

func TestOpenAIProvider_Embed_EmptyData(t *testing.T) {
	_, cfg := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"object": "list",
			"data":   []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	p, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}

	_, err = p.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty data array")
	}
}

// isErr is a helper equivalent to errors.Is for sentinel errors.
func isErr(err, target error) bool {
	// Use errors.Is via the standard library import.
	// We rely on the package-level errors import being present in the test file.
	// Since we're in the same package, we can call the helper directly.
	return errIs(err, target)
}

func errIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
