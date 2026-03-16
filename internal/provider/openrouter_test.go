package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microagent/internal/config"
)

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

// makeTestServer starts a test HTTP server and returns a ready-to-use ProviderConfig.
func makeTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, config.ProviderConfig) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, config.ProviderConfig{
		Type:       "openrouter",
		APIKey:     "test-key",
		Model:      "openrouter/free",
		BaseURL:    srv.URL,
		Timeout:    5 * time.Second,
		MaxRetries: 0,
	}
}

// orSuccessResp builds a minimal valid OpenRouter success response JSON string.
func orSuccessResp(content string, finishReason string) string {
	resp := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// orToolCallResp builds a response with tool calls and null content.
func orToolCallResp(toolCalls []map[string]any) string {
	resp := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": toolCalls,
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func writeORJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// --------------------------------------------------------------------------
// TestNormalizeFinishReason — pure function, no server needed
// --------------------------------------------------------------------------

func TestNormalizeFinishReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "content_filter"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalizeFinishReason(tc.in)
			if got != tc.want {
				t.Errorf("normalizeFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Name_SupportsTools — no server needed
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Name_SupportsTools(t *testing.T) {
	p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "k", Model: "m"})
	if p.Name() != "openrouter" {
		t.Errorf("Name() = %q, want openrouter", p.Name())
	}
	if !p.SupportsTools() {
		t.Error("SupportsTools() = false, want true")
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_HealthCheck
// --------------------------------------------------------------------------

func TestOpenRouterProvider_HealthCheck(t *testing.T) {
	t.Run("empty_key_returns_error", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{Model: "openrouter/free"})
		_, err := p.HealthCheck(context.Background())
		if err == nil {
			t.Fatal("expected error for empty api_key")
		}
	})
	t.Run("with_key_returns_model", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "sk-or-test", Model: "openrouter/free"})
		name, err := p.HealthCheck(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "openrouter/free" {
			t.Errorf("HealthCheck() = %q, want openrouter/free", name)
		}
	})
	t.Run("empty_model_defaults_to_openrouter_free", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "sk-or-test"})
		name, err := p.HealthCheck(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if name != "openrouter/free" {
			t.Errorf("HealthCheck() empty model = %q, want openrouter/free", name)
		}
	})
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Constructor
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Constructor(t *testing.T) {
	t.Run("default_base_url", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "k", Model: "m"})
		if p.baseURL != "https://openrouter.ai" {
			t.Errorf("baseURL = %q, want https://openrouter.ai", p.baseURL)
		}
	})
	t.Run("custom_base_url", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "k", BaseURL: "https://custom.example.com"})
		if p.baseURL != "https://custom.example.com" {
			t.Errorf("baseURL = %q, want https://custom.example.com", p.baseURL)
		}
	})
	t.Run("default_timeout", func(t *testing.T) {
		p := NewOpenRouterProvider(config.ProviderConfig{APIKey: "k"})
		if p.client.Timeout != 60*time.Second {
			t.Errorf("client.Timeout = %v, want 60s", p.client.Timeout)
		}
	})
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_HappyPath
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_HappyPath(t *testing.T) {
	srv, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", auth)
		}
		writeORJSON(w, orSuccessResp("Hello, world!", "stop"))
	})
	_ = srv

	p := NewOpenRouterProvider(cfg)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "Hello, world!" {
		t.Errorf("Content = %q, want Hello, world!", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls len = %d, want 0", len(resp.ToolCalls))
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_ToolCallResponse
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_ToolCallResponse(t *testing.T) {
	toolCalls := []map[string]any{
		{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      "shell",
				"arguments": `{"command":"ls"}`,
			},
		},
	}
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeORJSON(w, orToolCallResp(toolCalls))
	})

	p := NewOpenRouterProvider(cfg)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "run ls"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, want empty (null content)", resp.Content)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("ToolCall.ID = %q, want call_1", tc.ID)
	}
	if tc.Name != "shell" {
		t.Errorf("ToolCall.Name = %q, want shell", tc.Name)
	}
	// Input must be a valid JSON object {"command":"ls"}
	var inp map[string]any
	if err := json.Unmarshal(tc.Input, &inp); err != nil {
		t.Errorf("ToolCall.Input is not valid JSON: %v; got %s", err, tc.Input)
	}
	if inp["command"] != "ls" {
		t.Errorf("ToolCall.Input[command] = %v, want ls", inp["command"])
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_MultipleToolCalls
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_MultipleToolCalls(t *testing.T) {
	toolCalls := []map[string]any{
		{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "tool_a", "arguments": `{}`},
		},
		{
			"id": "call_2", "type": "function",
			"function": map[string]any{"name": "tool_b", "arguments": `{}`},
		},
	}
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeORJSON(w, orToolCallResp(toolCalls))
	})

	p := NewOpenRouterProvider(cfg)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "use tools"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" {
		t.Errorf("first tool call ID = %q, want call_1", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[1].ID != "call_2" {
		t.Errorf("second tool call ID = %q, want call_2", resp.ToolCalls[1].ID)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_FinishReasonMappings
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_FinishReasonMappings(t *testing.T) {
	cases := []struct {
		apiReason  string
		wantReason string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"content_filter", "content_filter"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.apiReason, func(t *testing.T) {
			_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeORJSON(w, orSuccessResp("text", tc.apiReason))
			})
			p := NewOpenRouterProvider(cfg)
			resp, err := p.Chat(context.Background(), ChatRequest{
				Messages: []ChatMessage{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			if resp.StopReason != tc.wantReason {
				t.Errorf("StopReason = %q, want %q", resp.StopReason, tc.wantReason)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_InvalidToolArguments
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_InvalidToolArguments(t *testing.T) {
	toolCalls := []map[string]any{
		{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": "not valid json"},
		},
	}
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeORJSON(w, orToolCallResp(toolCalls))
	})

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for invalid tool arguments JSON")
	}
	if !strings.Contains(err.Error(), "shell") {
		t.Errorf("error = %q, want it to mention tool name 'shell'", err.Error())
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_HTTP401_NoRetry
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_HTTP401_NoRetry(t *testing.T) {
	var calls int32
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	cfg.MaxRetries = 3 // set retries, but 401 should not retry

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("server called %d times, want exactly 1 (no retry on 401)", atomic.LoadInt32(&calls))
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_HTTP429_Retries
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_HTTP429_Retries(t *testing.T) {
	var calls int32
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	cfg.MaxRetries = 2 // 1 initial + 2 retries = 3 total; backoff: 2s + 4s

	p := NewOpenRouterProvider(cfg)
	// Timeout must exceed total backoff (2s + 4s = 6s) to allow all 3 calls.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := p.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	n := atomic.LoadInt32(&calls)
	if n != 3 {
		t.Errorf("server called %d times, want 3 (1 initial + 2 retries)", n)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_HTTP500_RetrySucceeds
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_HTTP500_RetrySucceeds(t *testing.T) {
	var calls int32
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeORJSON(w, orSuccessResp("recovered", "stop"))
	})
	cfg.MaxRetries = 3

	p := NewOpenRouterProvider(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := p.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want recovered", resp.Content)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("server called %d times, want 2", atomic.LoadInt32(&calls))
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_SystemPrompt
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeORJSON(w, orSuccessResp("ok", "stop"))
	})

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		SystemPrompt: "You are helpful",
		Messages:     []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(body.Messages) < 1 {
		t.Fatal("expected at least 1 message")
	}
	if body.Messages[0].Role != "system" {
		t.Errorf("messages[0].role = %q, want system", body.Messages[0].Role)
	}
	if body.Messages[0].Content != "You are helpful" {
		t.Errorf("messages[0].content = %v, want You are helpful", body.Messages[0].Content)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_NoSystemPrompt
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_NoSystemPrompt(t *testing.T) {
	var capturedBody []byte
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeORJSON(w, orSuccessResp("ok", "stop"))
	})

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(body.Messages) < 1 {
		t.Fatal("expected at least 1 message")
	}
	if body.Messages[0].Role != "user" {
		t.Errorf("messages[0].role = %q, want user (no system prefix)", body.Messages[0].Role)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_ToolDefinitions
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_ToolDefinitions(t *testing.T) {
	var capturedBody []byte
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeORJSON(w, orSuccessResp("ok", "stop"))
	})

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
		Tools: []ToolDefinition{
			{
				Name:        "shell_exec",
				Description: "Run a shell command",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	var body struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(body.Tools))
	}
	if body.Tools[0].Type != "function" {
		t.Errorf("tools[0].type = %q, want function", body.Tools[0].Type)
	}
	if body.Tools[0].Function.Name != "shell_exec" {
		t.Errorf("tools[0].function.name = %q, want shell_exec", body.Tools[0].Function.Name)
	}
	if body.Tools[0].Function.Description != "Run a shell command" {
		t.Errorf("tools[0].function.description = %q, want Run a shell command", body.Tools[0].Function.Description)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_ContextCancellation
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_ContextCancellation(t *testing.T) {
	unblock := make(chan struct{})
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-unblock:
		case <-time.After(5 * time.Second):
		}
	})
	defer close(unblock)

	p := NewOpenRouterProvider(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Chat() took %v, want <500ms (context not respected)", elapsed)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_NoToolsOmitsToolsKey
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_NoToolsOmitsToolsKey(t *testing.T) {
	var capturedBody []byte
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeORJSON(w, orSuccessResp("ok", "stop"))
	})

	p := NewOpenRouterProvider(cfg)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, ok := body["tools"]; ok {
		t.Error("tools key should be omitted when no tools provided")
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_Chat_AssistantNullContentOnToolCalls
// --------------------------------------------------------------------------

func TestOpenRouterProvider_Chat_AssistantNullContentOnToolCalls(t *testing.T) {
	var capturedBody []byte
	_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		writeORJSON(w, orSuccessResp("ok", "stop"))
	})

	p := NewOpenRouterProvider(cfg)
	// An assistant message with tool calls — content should be null in wire format
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "do something"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{ID: "call_1", Name: "shell", Input: json.RawMessage(`{"command":"ls"}`)},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	// Find the assistant message
	for _, msgRaw := range body.Messages {
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(msgRaw, &msg); err != nil {
			continue
		}
		roleBytes, ok := msg["role"]
		if !ok {
			continue
		}
		var role string
		if err := json.Unmarshal(roleBytes, &role); err != nil {
			continue
		}
		if role != "assistant" {
			continue
		}
		// content must be null
		contentRaw := msg["content"]
		if string(contentRaw) != "null" {
			t.Errorf("assistant message content = %s, want null", contentRaw)
		}
		return
	}
	t.Error("assistant message not found in request body")
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_ListFreeModels
// --------------------------------------------------------------------------

func TestOpenRouterProvider_ListFreeModels(t *testing.T) {
	t.Run("returns_only_free_models", func(t *testing.T) {
		modelsResp := `{"data":[
			{"id":"model-a","pricing":{"prompt":"0","completion":"0"}},
			{"id":"model-b","pricing":{"prompt":"0.001","completion":"0.002"}},
			{"id":"model-c","pricing":{"prompt":"0","completion":"0"}}
		]}`
		_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeORJSON(w, modelsResp)
		})
		p := NewOpenRouterProvider(cfg)
		models, err := p.ListFreeModels(context.Background())
		if err != nil {
			t.Fatalf("ListFreeModels() error: %v", err)
		}
		if len(models) != 2 {
			t.Fatalf("len(models) = %d, want 2", len(models))
		}
		if models[0] != "model-a" || models[1] != "model-c" {
			t.Errorf("models = %v, want [model-a, model-c]", models)
		}
	})

	t.Run("empty_list_returns_empty_slice_not_nil", func(t *testing.T) {
		_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			writeORJSON(w, `{"data":[]}`)
		})
		p := NewOpenRouterProvider(cfg)
		models, err := p.ListFreeModels(context.Background())
		if err != nil {
			t.Fatalf("ListFreeModels() error: %v", err)
		}
		if models == nil {
			t.Error("expected empty slice, got nil")
		}
		if len(models) != 0 {
			t.Errorf("len(models) = %d, want 0", len(models))
		}
	})

	t.Run("http_error_returns_error", func(t *testing.T) {
		_, cfg := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		p := NewOpenRouterProvider(cfg)
		_, err := p.ListFreeModels(context.Background())
		if err == nil {
			t.Fatal("expected error for 401 from models endpoint")
		}
	})
}

// --------------------------------------------------------------------------
// TestOpenRouterProvider_InterfaceAssertion
// --------------------------------------------------------------------------

// Verifies that the compile-time interface assertion `var _ Provider = (*OpenRouterProvider)(nil)`
// is present and the type fully satisfies Provider. The test itself is the compilation.
func TestOpenRouterProvider_InterfaceAssertion(t *testing.T) {
	var _ Provider = (*OpenRouterProvider)(nil)
}
