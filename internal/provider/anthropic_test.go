package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/content"
)

// ---- helpers ----------------------------------------------------------------

func newTestProvider(t *testing.T, ts *httptest.Server, overrides ...func(*config.ProviderConfig)) *AnthropicProvider {
	t.Helper()
	cfg := config.ProviderConfig{
		APIKey:     "test-key",
		BaseURL:    ts.URL,
		MaxRetries: 1,
		Timeout:    5 * time.Second,
	}
	for _, fn := range overrides {
		fn(&cfg)
	}
	return NewAnthropicProvider(cfg)
}

func minimalRequest() ChatRequest {
	return ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hello")}},
	}
}

// buildSuccessBody returns a JSON-encoded anthropic success response with a
// single text block.
func buildSuccessBody(text string) string {
	resp := map[string]any{
		"type":        "message",
		"role":        "assistant",
		"stop_reason": "end_turn",
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// ---- TestNewAnthropicProvider_Defaults --------------------------------------

func TestNewAnthropicProvider_Defaults(t *testing.T) {
	cfg := config.ProviderConfig{
		APIKey: "key",
		// Timeout == 0 → should default to 60s
		// Model == ""  → should default inside Chat()
	}
	p := NewAnthropicProvider(cfg)
	if p.client.Timeout != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", p.client.Timeout)
	}
}

// ---- TestAnthropicProvider_Interface ----------------------------------------

func TestAnthropicProvider_Interface(t *testing.T) {
	p := NewAnthropicProvider(config.ProviderConfig{APIKey: "k"})
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want %q", p.Name(), "anthropic")
	}
	if !p.SupportsTools() {
		t.Error("SupportsTools() returned false, want true")
	}
}

// ---- TestAnthropicProvider_RequiredHeaders ----------------------------------

func TestAnthropicProvider_RequiredHeaders(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	_, err := prov.Chat(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if capturedReq == nil {
		t.Fatal("server never received a request")
	}
	if got := capturedReq.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q, want %q", got, "test-key")
	}
	if got := capturedReq.Header.Get("anthropic-version"); got == "" {
		t.Error("anthropic-version header not set")
	}
	if got := capturedReq.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}
}

// ---- TestAnthropicProvider_RequestMapping -----------------------------------

func TestAnthropicProvider_RequestMapping(t *testing.T) {
	tests := []struct {
		name     string
		messages []ChatMessage
		verify   func(t *testing.T, body map[string]any)
	}{
		{
			name:     "simple user message",
			messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
			verify: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]any)
				if len(msgs) != 1 {
					t.Fatalf("expected 1 message, got %d", len(msgs))
				}
				msg := msgs[0].(map[string]any)
				if msg["role"] != "user" {
					t.Errorf("role = %v, want user", msg["role"])
				}
				// content.Blocks marshals as a JSON array of content blocks
				blocks, ok := msg["content"].([]any)
				if !ok || len(blocks) == 0 {
					t.Fatalf("expected content to be a non-empty array, got %v", msg["content"])
				}
				b0 := blocks[0].(map[string]any)
				if b0["type"] != "text" {
					t.Errorf("block[0] type = %v, want text", b0["type"])
				}
				if b0["text"] != "hi" {
					t.Errorf("block[0] text = %v, want hi", b0["text"])
				}
			},
		},
		{
			name: "assistant message with tool calls",
			messages: []ChatMessage{
				{Role: "user", Content: content.TextBlock("do something")},
				{
					Role:    "assistant",
					Content: nil,
					ToolCalls: []ToolCall{
						{ID: "tc1", Name: "shell_exec", Input: json.RawMessage(`{"command":"ls"}`)},
					},
				},
			},
			verify: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]any)
				// last message should be the assistant with tool_use content array
				last := msgs[len(msgs)-1].(map[string]any)
				if last["role"] != "assistant" {
					t.Errorf("last message role = %v, want assistant", last["role"])
				}
				content := last["content"].([]any)
				found := false
				for _, c := range content {
					cm := c.(map[string]any)
					if cm["type"] == "tool_use" {
						found = true
						if cm["id"] != "tc1" {
							t.Errorf("tool_use id = %v, want tc1", cm["id"])
						}
						if cm["name"] != "shell_exec" {
							t.Errorf("tool_use name = %v, want shell_exec", cm["name"])
						}
					}
				}
				if !found {
					t.Error("no tool_use block found in assistant message content")
				}
			},
		},
		{
			name: "standalone tool result message",
			messages: []ChatMessage{
				{Role: "tool", Content: content.TextBlock("hello"), ToolCallID: "tc1"},
			},
			verify: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]any)
				if len(msgs) != 1 {
					t.Fatalf("expected 1 message, got %d", len(msgs))
				}
				msg := msgs[0].(map[string]any)
				if msg["role"] != "user" {
					t.Errorf("role = %v, want user", msg["role"])
				}
				content := msg["content"].([]any)
				if len(content) == 0 {
					t.Fatal("expected content array to be non-empty")
				}
				block := content[0].(map[string]any)
				if block["type"] != "tool_result" {
					t.Errorf("block type = %v, want tool_result", block["type"])
				}
				if block["tool_use_id"] != "tc1" {
					t.Errorf("tool_use_id = %v, want tc1", block["tool_use_id"])
				}
			},
		},
		{
			name: "tool result after user message merges",
			messages: []ChatMessage{
				{Role: "user", Content: content.TextBlock("run it")},
				{Role: "tool", Content: content.TextBlock("done"), ToolCallID: "tc2"},
			},
			verify: func(t *testing.T, body map[string]any) {
				msgs := body["messages"].([]any)
				// Should be a single user message with merged content
				if len(msgs) != 1 {
					t.Fatalf("expected 1 merged user message, got %d", len(msgs))
				}
				msg := msgs[0].(map[string]any)
				if msg["role"] != "user" {
					t.Errorf("role = %v, want user", msg["role"])
				}
				content := msg["content"].([]any)
				if len(content) != 2 {
					t.Fatalf("expected 2 content blocks in merged user msg, got %d", len(content))
				}
				// first block should be text
				b0 := content[0].(map[string]any)
				if b0["type"] != "text" {
					t.Errorf("first block type = %v, want text", b0["type"])
				}
				// second block should be tool_result
				b1 := content[1].(map[string]any)
				if b1["type"] != "tool_result" {
					t.Errorf("second block type = %v, want tool_result", b1["type"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedBody []byte
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				capturedBody, err = readAll(r.Body)
				if err != nil {
					t.Logf("read body error: %v", err)
				}
				writeJSON(w, buildSuccessBody("ok"))
			}))
			defer ts.Close()

			prov := newTestProvider(t, ts)
			_, err := prov.Chat(context.Background(), ChatRequest{Messages: tc.messages})
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			var bodyMap map[string]any
			if err := json.Unmarshal(capturedBody, &bodyMap); err != nil {
				t.Fatalf("could not unmarshal request body: %v\nbody: %s", err, capturedBody)
			}
			tc.verify(t, bodyMap)
		})
	}
}

// readAll reads all bytes from a reader.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 512)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return out, nil
}

// ---- TestAnthropicProvider_DefaultModelAndMaxTokens -------------------------

func TestAnthropicProvider_DefaultModelAndMaxTokens(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	// empty model, zero MaxTokens
	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.Model = ""
	})
	req := ChatRequest{
		Messages:  []ChatMessage{{Role: "user", Content: content.TextBlock("hello")}},
		MaxTokens: 0,
	}
	_, err := prov.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body["model"] != "claude-3-5-sonnet-20241022" {
		t.Errorf("model = %v, want claude-3-5-sonnet-20241022", body["model"])
	}
	if body["max_tokens"].(float64) != 4096 {
		t.Errorf("max_tokens = %v, want 4096", body["max_tokens"])
	}
}

// ---- TestAnthropicProvider_ResponseParsing ----------------------------------

func TestAnthropicProvider_ResponseParsing(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
		wantContent  string
		wantTools    int
	}{
		{
			name:         "text only",
			responseBody: buildSuccessBody("hello world"),
			wantContent:  "hello world",
			wantTools:    0,
		},
		{
			name: "multiple text blocks concatenated",
			responseBody: func() string {
				resp := map[string]any{
					"type":        "message",
					"role":        "assistant",
					"stop_reason": "end_turn",
					"content": []any{
						map[string]any{"type": "text", "text": "hello"},
						map[string]any{"type": "text", "text": "world"},
					},
					"usage": map[string]any{},
				}
				b, _ := json.Marshal(resp)
				return string(b)
			}(),
			wantContent: "hello\nworld",
			wantTools:   0,
		},
		{
			name: "tool_use response",
			responseBody: func() string {
				resp := map[string]any{
					"type":        "message",
					"role":        "assistant",
					"stop_reason": "tool_use",
					"content": []any{
						map[string]any{
							"type":  "tool_use",
							"id":    "tu1",
							"name":  "shell_exec",
							"input": map[string]any{"command": "ls"},
						},
					},
					"usage": map[string]any{},
				}
				b, _ := json.Marshal(resp)
				return string(b)
			}(),
			wantContent: "",
			wantTools:   1,
		},
		{
			name: "multiple tool calls",
			responseBody: func() string {
				resp := map[string]any{
					"type":        "message",
					"role":        "assistant",
					"stop_reason": "tool_use",
					"content": []any{
						map[string]any{
							"type":  "tool_use",
							"id":    "tu1",
							"name":  "tool_a",
							"input": map[string]any{},
						},
						map[string]any{
							"type":  "tool_use",
							"id":    "tu2",
							"name":  "tool_b",
							"input": map[string]any{},
						},
					},
					"usage": map[string]any{},
				}
				b, _ := json.Marshal(resp)
				return string(b)
			}(),
			wantContent: "",
			wantTools:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, tc.responseBody)
			}))
			defer ts.Close()

			prov := newTestProvider(t, ts)
			resp, err := prov.Chat(context.Background(), minimalRequest())
			if err != nil {
				t.Fatalf("Chat() error: %v", err)
			}
			if resp.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", resp.Content, tc.wantContent)
			}
			if len(resp.ToolCalls) != tc.wantTools {
				t.Errorf("ToolCalls len = %d, want %d", len(resp.ToolCalls), tc.wantTools)
			}
		})
	}
}

// ---- TestAnthropicProvider_RetryOn429 ---------------------------------------

func TestAnthropicProvider_RetryOn429(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, buildSuccessBody("recovered"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.MaxRetries = 3
	})
	// Use a context with a generous timeout but short enough to not hang the test suite.
	// The retry delays are 2s, 4s, 6s... We cancel after 2 retries succeed on attempt 3,
	// so actual delay is 2s (attempt 0 fail) + 4s (attempt 1 fail) but success on attempt 2.
	// Use context cancellation so the select wakes up fast if needed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := prov.Chat(ctx, minimalRequest())
	if err != nil {
		t.Fatalf("Chat() error after retries: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("Content = %q, want recovered", resp.Content)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("server hit %d times, want 3", atomic.LoadInt32(&attempts))
	}
}

// ---- TestAnthropicProvider_RetryExhaustion ----------------------------------

func TestAnthropicProvider_RetryExhaustion(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.MaxRetries = 2
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := prov.Chat(ctx, minimalRequest())
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	// MaxRetries=2 means 1 initial + 2 retries = 3 total
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("server hit %d times, want 3", atomic.LoadInt32(&attempts))
	}
}

// ---- TestAnthropicProvider_NoRetryOn400 -------------------------------------

func TestAnthropicProvider_NoRetryOn400(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.MaxRetries = 3
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := prov.Chat(ctx, minimalRequest())
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("server hit %d times, want exactly 1 (no retry on 4xx)", atomic.LoadInt32(&attempts))
	}
}

// ---- TestAnthropicProvider_InvalidJSON --------------------------------------

func TestAnthropicProvider_InvalidJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json}`))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	_, err := prov.Chat(context.Background(), minimalRequest())
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
	if !strings.Contains(err.Error(), "parsing anthropic response") {
		t.Errorf("error = %q, want it to contain 'parsing anthropic response'", err.Error())
	}
}

// ---- TestAnthropicProvider_ContextCancellation ------------------------------

func TestAnthropicProvider_ContextCancellation(t *testing.T) {
	// Use a channel to synchronize: the server handler blocks until we signal it.
	// We signal after the client context is cancelled and Chat() returns.
	unblock := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the test signals us to return (after client cancelled)
		select {
		case <-unblock:
		case <-time.After(5 * time.Second):
		}
	}))
	// Defer order: signal unblock first, then close server (so handler exits cleanly).
	defer ts.Close()
	defer close(unblock)

	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.Timeout = 10 * time.Second // allow client to use the context deadline
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := prov.Chat(ctx, minimalRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Chat() took %v, want <500ms (context not respected)", elapsed)
	}
}

// ---- TestAnthropicProvider_APIErrorInBody -----------------------------------

func TestAnthropicProvider_APIErrorInBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"error":{"type":"invalid_request_error","message":"bad param"}}`)
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	_, err := prov.Chat(context.Background(), minimalRequest())
	if err == nil {
		t.Fatal("expected error from API error body")
	}
	if !strings.Contains(err.Error(), "bad param") {
		t.Errorf("error = %q, want it to contain 'bad param'", err.Error())
	}
}

// ---- TestAnthropicProvider_ContextCancelDuringRetry -------------------------

func TestAnthropicProvider_ContextCancelDuringRetry(t *testing.T) {
	// Server always returns 500 to trigger retries
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.MaxRetries = 10
	})

	// Cancel context quickly — should abort during retry sleep
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := prov.Chat(ctx, minimalRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	// The retry sleep is context-aware; should return well before the full sleep duration
	if elapsed > 500*time.Millisecond {
		t.Errorf("Chat() took %v, want <500ms during context-aware retry sleep", elapsed)
	}
}

// ---- TestAnthropicProvider_DefaultMaxRetries --------------------------------

// When MaxRetries < 1 in config, the provider forces it to 1 (maxRetries = 1 branch).
func TestAnthropicProvider_DefaultMaxRetries(t *testing.T) {
	var attempts int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	// MaxRetries=0 → forced to 1 inside Chat()
	prov := newTestProvider(t, ts, func(c *config.ProviderConfig) {
		c.MaxRetries = 0
	})
	_, err := prov.Chat(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
}

// ---- TestAnthropicProvider_AssistantWithTextAndToolCalls --------------------

// Covers the branch where an assistant message has both text content AND tool calls.
func TestAnthropicProvider_AssistantWithTextAndToolCalls(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	req := ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: content.TextBlock("hi")},
			{
				Role:    "assistant",
				Content: content.TextBlock("I'll use a tool"),
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "shell_exec", Input: json.RawMessage(`{"command":"ls"}`)},
				},
			},
		},
	}
	_, err := prov.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := body["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	content := last["content"].([]any)
	// Expect text block AND tool_use block
	hasText, hasToolUse := false, false
	for _, c := range content {
		cm := c.(map[string]any)
		if cm["type"] == "text" {
			hasText = true
		}
		if cm["type"] == "tool_use" {
			hasToolUse = true
		}
	}
	if !hasText {
		t.Error("expected text block in assistant content")
	}
	if !hasToolUse {
		t.Error("expected tool_use block in assistant content")
	}
}

// ---- TestAnthropicProvider_ToolResultMergesIntoExistingArray ----------------

// Covers the `case []any: append(v, block)` branch when previous user message content
// is already a []any (i.e., a second tool result merging into an already-arrayed user msg).
func TestAnthropicProvider_ToolResultMergesIntoExistingArray(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	// First tool result creates []any content on user message.
	// Second tool result should append to that []any.
	req := ChatRequest{
		Messages: []ChatMessage{
			{Role: "tool", Content: content.TextBlock("result1"), ToolCallID: "tc1"},
			{Role: "tool", Content: content.TextBlock("result2"), ToolCallID: "tc2"},
		},
	}
	_, err := prov.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := body["messages"].([]any)
	// Both tool results should be merged into a single user message
	if len(msgs) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(msgs))
	}
	msg := msgs[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %d", len(content))
	}
}

// ---- TestAnthropicProvider_WithToolDefinitions ------------------------------

// Covers the tools-mapping loop (apiReq.Tools = append(...)).
func TestAnthropicProvider_WithToolDefinitions(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	req := ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
		Tools: []ToolDefinition{
			{
				Name:        "shell_exec",
				Description: "Run a shell command",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
		},
	}
	_, err := prov.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatal("expected tools array in request body")
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "shell_exec" {
		t.Errorf("tool name = %v, want shell_exec", tool["name"])
	}
}

// ---- TestAnthropicProvider_NetworkErrorRetry --------------------------------

// Covers the network-error retry path (err != nil from client.Do → delay → continue).
// Also covers "failed after N attempts" final error.
func TestAnthropicProvider_NetworkErrorRetry(t *testing.T) {
	// Create a server, get its address, then close it so connections fail immediately
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close() // close immediately so subsequent requests get connection refused

	prov := NewAnthropicProvider(config.ProviderConfig{
		APIKey:     "key",
		BaseURL:    url,
		MaxRetries: 1, // 1 retry = 2 total attempts
		Timeout:    1 * time.Second,
	})

	// Use a context that cancels quickly to avoid full retry sleep
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := prov.Chat(ctx, minimalRequest())
	if err == nil {
		t.Fatal("expected error from connection refused")
	}
}

// ---- TestAnthropicProvider_ToolCallDetails ----------------------------------

func TestAnthropicProvider_ToolCallDetails(t *testing.T) {
	body := func() string {
		resp := map[string]any{
			"type":        "message",
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "call-123",
					"name":  "shell_exec",
					"input": map[string]any{"command": "echo hi"},
				},
			},
			"usage": map[string]any{},
		}
		b, _ := json.Marshal(resp)
		return string(b)
	}()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, body)
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts)
	resp, err := prov.Chat(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call-123" {
		t.Errorf("ToolCall.ID = %q, want call-123", tc.ID)
	}
	if tc.Name != "shell_exec" {
		t.Errorf("ToolCall.Name = %q, want shell_exec", tc.Name)
	}
	// Input should be valid JSON
	var inp map[string]any
	if err := json.Unmarshal(tc.Input, &inp); err != nil {
		t.Errorf("ToolCall.Input is not valid JSON: %v", err)
	}
}

// ---- TestAnthropicMultimodalRequest ----------------------------------------

// stubMediaReader is a minimal mediaReader for tests that returns fixed bytes
// keyed by sha256. It satisfies the mediaReader interface without importing any
// store package.
type stubMediaReader struct {
	// entries maps sha256 → (bytes, mime)
	entries map[string]stubMediaEntry
}

type stubMediaEntry struct {
	data []byte
	mime string
}

func (s *stubMediaReader) GetMedia(_ context.Context, sha256 string) ([]byte, string, error) {
	if e, ok := s.entries[sha256]; ok {
		return e.data, e.mime, nil
	}
	return nil, "", nil
}

// TestAnthropicMultimodalRequest verifies that a user message containing a
// BlockText + BlockImage is translated to the correct Anthropic API wire shape.
// The expected shape is stored in testdata/anthropic_multimodal_request.json.
// Comparison is map-based (JSON unmarshal → reflect.DeepEqual equivalent via
// re-marshal) so key ordering does not cause false failures.
func TestAnthropicMultimodalRequest(t *testing.T) {
	// JPEG magic bytes — a plausible image payload for testing.
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0}

	stub := &stubMediaReader{
		entries: map[string]stubMediaEntry{
			"abc123": {data: jpegMagic, mime: "image/jpeg"},
		},
	}

	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = readAll(r.Body)
		if err != nil {
			t.Logf("read body error: %v", err)
		}
		writeJSON(w, buildSuccessBody("ok"))
	}))
	defer ts.Close()

	prov := newTestProvider(t, ts).WithMediaReader(stub)

	req := ChatRequest{
		Messages: []ChatMessage{
			{
				Role: "user",
				Content: content.Blocks{
					{Type: content.BlockText, Text: "here is a photo"},
					{Type: content.BlockImage, MediaSHA256: "abc123", MIME: "image/jpeg", Size: 1024},
				},
			},
		},
	}

	_, err := prov.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}

	// Unmarshal actual body as a generic map for key-order-independent comparison.
	var actual map[string]any
	if err := json.Unmarshal(capturedBody, &actual); err != nil {
		t.Fatalf("unmarshal actual body: %v", err)
	}

	// Load golden file.
	goldenPath := "testdata/anthropic_multimodal_request.json"
	goldenRaw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file %s: %v", goldenPath, err)
	}
	var golden map[string]any
	if err := json.Unmarshal(goldenRaw, &golden); err != nil {
		t.Fatalf("unmarshal golden file: %v", err)
	}

	// Re-marshal both to canonical JSON for a stable string comparison.
	actualCanon, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("re-marshal actual: %v", err)
	}
	goldenCanon, err := json.Marshal(golden)
	if err != nil {
		t.Fatalf("re-marshal golden: %v", err)
	}

	if string(actualCanon) != string(goldenCanon) {
		t.Errorf("multimodal request body does not match golden file %s\n\ngot:\n%s\n\nwant:\n%s",
			goldenPath, capturedBody, goldenRaw)
	}
}

// --------------------------------------------------------------------------
// Phase 2.1 — thinkingShape capability map
// --------------------------------------------------------------------------

func TestAnthropicThinkingCapability_Map(t *testing.T) {
	tests := []struct {
		modelID string
		want    thinkingShape
	}{
		{"claude-opus-4-7", thinkingAdaptive},
		{"claude-opus-4-6", thinkingManual},
		{"claude-sonnet-4-6", thinkingManual},
		{"claude-haiku-4-5-20251001", thinkingNone},
		{"claude-opus-4-5-20251101", thinkingManual},
		{"claude-sonnet-4-5-20250929", thinkingManual},
		{"claude-opus-4-1-20250805", thinkingManual},
		{"unknown-model-xyz", thinkingNone},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := anthropicThinkingCapability[tt.modelID]
			if got != tt.want {
				t.Errorf("anthropicThinkingCapability[%q] = %v, want %v", tt.modelID, got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Phase 2.2 — anthropicThinkingParams() pure helper
// --------------------------------------------------------------------------

func TestAnthropicThinkingParams(t *testing.T) {
	tests := []struct {
		name         string
		modelID      string
		effort       string
		budgetTokens int
		wantNil      bool
		wantMap      map[string]any
	}{
		{
			name:         "adaptive model returns adaptive payload",
			modelID:      "claude-opus-4-7",
			effort:       "high",
			budgetTokens: 10000,
			wantMap:      map[string]any{"type": "adaptive", "effort": "high"},
		},
		{
			name:         "manual model returns enabled payload with budget",
			modelID:      "claude-opus-4-6",
			effort:       "medium",
			budgetTokens: 15000,
			wantMap:      map[string]any{"type": "enabled", "budget_tokens": 15000},
		},
		{
			name:         "non-thinking model returns nil",
			modelID:      "claude-haiku-4-5-20251001",
			effort:       "high",
			budgetTokens: 10000,
			wantNil:      true,
		},
		{
			name:         "unknown model returns nil",
			modelID:      "claude-unknown-xyz",
			effort:       "high",
			budgetTokens: 10000,
			wantNil:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := anthropicThinkingParams(tt.modelID, tt.effort, tt.budgetTokens)
			if tt.wantNil {
				if got != nil {
					t.Errorf("anthropicThinkingParams() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("anthropicThinkingParams() = nil, want %v", tt.wantMap)
			}
			for k, wantV := range tt.wantMap {
				gotV, ok := got[k]
				if !ok {
					t.Errorf("key %q missing from result %v", k, got)
					continue
				}
				if gotV != wantV {
					t.Errorf("got[%q] = %v (%T), want %v (%T)", k, gotV, gotV, wantV, wantV)
				}
			}
		})
	}
}

// --------------------------------------------------------------------------
// Phase 2.3 — Thinking params injection in buildAnthropicRequest
// --------------------------------------------------------------------------

func buildAnthropicBodyForModel(t *testing.T, modelID string, effort string, budgetTokens int) map[string]any {
	t.Helper()
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(buildSuccessBody("ok")))
	}))
	t.Cleanup(ts.Close)

	creds := config.ProviderCredentials{
		ThinkingEffort:       effort,
		ThinkingBudgetTokens: &budgetTokens,
	}
	p := NewAnthropicProvider(config.ProviderConfig{
		APIKey:      "test-key",
		BaseURL:     ts.URL,
		Model:       modelID,
		MaxRetries:  0,
		Timeout:     5 * time.Second,
	})
	p.SetThinkingConfig(creds)

	_, _ = p.Chat(context.Background(), minimalRequest())

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	return body
}

func TestAnthropicBuildRequest_ThinkingInjection(t *testing.T) {
	t.Run("RS-3b: adaptive model gets adaptive thinking payload", func(t *testing.T) {
		budget := 10000
		body := buildAnthropicBodyForModel(t, "claude-opus-4-7", "high", budget)
		thinking, ok := body["thinking"]
		if !ok {
			t.Fatal("expected 'thinking' key in request body")
		}
		thinkingMap, ok := thinking.(map[string]any)
		if !ok {
			t.Fatalf("thinking is not a map: %T", thinking)
		}
		if thinkingMap["type"] != "adaptive" {
			t.Errorf("thinking.type = %q, want %q", thinkingMap["type"], "adaptive")
		}
		if thinkingMap["effort"] != "high" {
			t.Errorf("thinking.effort = %q, want %q", thinkingMap["effort"], "high")
		}
		if _, hasBudget := thinkingMap["budget_tokens"]; hasBudget {
			t.Error("adaptive thinking should NOT contain budget_tokens")
		}
	})

	t.Run("RS-3c: manual model gets enabled thinking payload with budget_tokens", func(t *testing.T) {
		budget := 15000
		body := buildAnthropicBodyForModel(t, "claude-opus-4-6", "medium", budget)
		thinking, ok := body["thinking"]
		if !ok {
			t.Fatal("expected 'thinking' key in request body")
		}
		thinkingMap, ok := thinking.(map[string]any)
		if !ok {
			t.Fatalf("thinking is not a map: %T", thinking)
		}
		if thinkingMap["type"] != "enabled" {
			t.Errorf("thinking.type = %q, want %q", thinkingMap["type"], "enabled")
		}
		// JSON numbers unmarshal as float64
		gotBudget, ok := thinkingMap["budget_tokens"].(float64)
		if !ok {
			t.Fatalf("budget_tokens is not a number: %T %v", thinkingMap["budget_tokens"], thinkingMap["budget_tokens"])
		}
		if int(gotBudget) != 15000 {
			t.Errorf("budget_tokens = %v, want 15000", gotBudget)
		}
	})

	t.Run("RS-3a: non-thinking model has no thinking key", func(t *testing.T) {
		budget := 10000
		body := buildAnthropicBodyForModel(t, "claude-haiku-4-5-20251001", "high", budget)
		if _, ok := body["thinking"]; ok {
			t.Error("non-thinking model should NOT have 'thinking' key in request")
		}
	})
}
