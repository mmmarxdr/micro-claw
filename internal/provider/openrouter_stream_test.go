package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/content"
)

// --------------------------------------------------------------------------
// Test helpers — SSE response builders
// --------------------------------------------------------------------------

// makeStreamServer starts a test HTTP server that writes SSE lines and returns a config.
func makeStreamServer(t *testing.T, sseLines string) (*httptest.Server, config.ProviderConfig) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify stream: true is in the request body.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v, want true", body["stream"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseLines))
	}))
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

// sseData formats a single SSE data frame with trailing double newline.
func sseData(jsonStr string) string {
	return "data: " + jsonStr + "\n\n"
}

// sseDone returns the [DONE] sentinel frame.
func sseDone() string {
	return "data: [DONE]\n\n"
}

// collectEvents drains events from a StreamResult and returns them.
func collectEvents(t *testing.T, sr *StreamResult) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for ev := range sr.Events {
		events = append(events, ev)
	}
	return events
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_TextOnly — simple text streaming
// --------------------------------------------------------------------------

func TestOpenRouterStream_TextOnly(t *testing.T) {
	sse := sseData(`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"content":" world"},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Verify event sequence: TextDelta, TextDelta, Usage, Done
	wantTypes := []StreamEventType{
		StreamEventTextDelta,
		StreamEventTextDelta,
		StreamEventUsage,
		StreamEventDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d; events: %v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("events[%d].Type = %v, want %v", i, events[i].Type, want)
		}
	}

	// Verify text content.
	if events[0].Text != "Hello" {
		t.Errorf("events[0].Text = %q, want Hello", events[0].Text)
	}
	if events[1].Text != " world" {
		t.Errorf("events[1].Text = %q, want ' world'", events[1].Text)
	}

	// Verify usage.
	usageEv := events[2]
	if usageEv.Usage == nil {
		t.Fatal("usage event has nil Usage")
	}
	if usageEv.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", usageEv.Usage.InputTokens)
	}
	if usageEv.Usage.OutputTokens != 2 {
		t.Errorf("OutputTokens = %d, want 2", usageEv.Usage.OutputTokens)
	}
	if usageEv.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", usageEv.StopReason)
	}

	// Verify assembled response.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Hello world" {
		t.Errorf("Content = %q, want 'Hello world'", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_ToolCall — tool call streaming
// --------------------------------------------------------------------------

func TestOpenRouterStream_ToolCall(t *testing.T) {
	sse := sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"shell_exec","arguments":""}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":8}}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("run ls")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Expected: ToolCallStart, ToolCallDelta, ToolCallDelta, ToolCallEnd, Usage, Done
	wantTypes := []StreamEventType{
		StreamEventToolCallStart,
		StreamEventToolCallDelta,
		StreamEventToolCallDelta,
		StreamEventToolCallEnd,
		StreamEventUsage,
		StreamEventDone,
	}
	if len(events) != len(wantTypes) {
		var names []string
		for _, e := range events {
			names = append(names, e.Type.String())
		}
		t.Fatalf("got %d events %v, want %d %v", len(events), names, len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("events[%d].Type = %v, want %v", i, events[i].Type, want)
		}
	}

	// Verify tool call start fields.
	if events[0].ToolCallID != "call_123" {
		t.Errorf("ToolCallID = %q, want call_123", events[0].ToolCallID)
	}
	if events[0].ToolName != "shell_exec" {
		t.Errorf("ToolName = %q, want shell_exec", events[0].ToolName)
	}

	// Verify assembled response.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("ToolCall.ID = %q, want call_123", tc.ID)
	}
	if tc.Name != "shell_exec" {
		t.Errorf("ToolCall.Name = %q, want shell_exec", tc.Name)
	}
	var inp map[string]any
	if err := json.Unmarshal(tc.Input, &inp); err != nil {
		t.Errorf("ToolCall.Input is not valid JSON: %v; got %s", err, tc.Input)
	}
	if inp["cmd"] != "ls" {
		t.Errorf("ToolCall.Input[cmd] = %v, want ls", inp["cmd"])
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_MixedContent — text + tool call in same response
// --------------------------------------------------------------------------

func TestOpenRouterStream_MixedContent(t *testing.T) {
	sse := sseData(`{"choices":[{"delta":{"content":"Let me run that."},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_456","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10}}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("do it")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Expected: TextDelta, ToolCallStart, ToolCallDelta, ToolCallEnd, Usage, Done
	wantTypes := []StreamEventType{
		StreamEventTextDelta,
		StreamEventToolCallStart,
		StreamEventToolCallDelta,
		StreamEventToolCallEnd,
		StreamEventUsage,
		StreamEventDone,
	}
	if len(events) != len(wantTypes) {
		var names []string
		for _, e := range events {
			names = append(names, e.Type.String())
		}
		t.Fatalf("got %d events %v, want %d", len(events), names, len(wantTypes))
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Errorf("events[%d].Type = %v, want %v", i, events[i].Type, want)
		}
	}

	// Verify assembled response has both text and tool call.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Let me run that." {
		t.Errorf("Content = %q, want 'Let me run that.'", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "shell" {
		t.Errorf("ToolCall.Name = %q, want shell", resp.ToolCalls[0].Name)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_DoneHandling — [DONE] stops reading
// --------------------------------------------------------------------------

func TestOpenRouterStream_DoneHandling(t *testing.T) {
	// Ensure [DONE] causes clean completion even without a finish_reason chunk.
	sse := sseData(`{"choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`) +
		sseDone() +
		// Anything after [DONE] should be ignored by ParseSSE (it won't call onEvent after [DONE] returns).
		sseData(`{"choices":[{"delta":{"content":"SHOULD NOT APPEAR"},"finish_reason":null}]}`)

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Should NOT contain any text after the Done event.
	for _, ev := range events {
		if ev.Type == StreamEventTextDelta && ev.Text == "SHOULD NOT APPEAR" {
			t.Error("received text after [DONE] — ParseSSE should have stopped")
		}
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if strings.Contains(resp.Content, "SHOULD NOT APPEAR") {
		t.Error("assembled response contains text after [DONE]")
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_HTTPError — non-200 status returns error
// --------------------------------------------------------------------------

func TestOpenRouterStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.ProviderConfig{
		Type:    "openrouter",
		APIKey:  "bad-key",
		Model:   "openrouter/free",
		BaseURL: srv.URL,
	}
	p := NewOpenRouterProvider(cfg)

	_, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want it to contain '401'", err.Error())
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_MalformedJSON — parse error mid-stream
// --------------------------------------------------------------------------

func TestOpenRouterStream_MalformedJSON(t *testing.T) {
	sse := sseData(`{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`) +
		sseData(`{invalid json}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Should contain at least a TextDelta and an Error.
	var hasText, hasError bool
	for _, ev := range events {
		if ev.Type == StreamEventTextDelta {
			hasText = true
		}
		if ev.Type == StreamEventError {
			hasError = true
		}
	}
	if !hasText {
		t.Error("expected at least one TextDelta event before the error")
	}
	if !hasError {
		t.Error("expected an Error event for malformed JSON")
	}

	// Response should return an error.
	_, err = sr.Response()
	if err == nil {
		t.Error("expected Response() to return error after malformed JSON")
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_MultipleToolCalls — two tool calls in same response
// --------------------------------------------------------------------------

func TestOpenRouterStream_MultipleToolCalls(t *testing.T) {
	sse := sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"tool_a","arguments":""}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"tool_b","arguments":""}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"x\":1}"}}]},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("use tools")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Count tool call starts.
	var starts int
	for _, ev := range events {
		if ev.Type == StreamEventToolCallStart {
			starts++
		}
	}
	if starts != 2 {
		t.Errorf("got %d ToolCallStart events, want 2", starts)
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "tool_a" {
		t.Errorf("ToolCalls[0].Name = %q, want tool_a", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[1].Name != "tool_b" {
		t.Errorf("ToolCalls[1].Name = %q, want tool_b", resp.ToolCalls[1].Name)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_ContextCancellation — context cancel stops stream
// --------------------------------------------------------------------------

func TestOpenRouterStream_ContextCancellation(t *testing.T) {
	// Server that streams slowly, one chunk per 100ms.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement Flusher")
			return
		}
		for i := 0; i < 100; i++ {
			chunk := fmt.Sprintf(`{"choices":[{"delta":{"content":"tok%d "},"finish_reason":null}]}`, i)
			_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := config.ProviderConfig{
		Type:    "openrouter",
		APIKey:  "test-key",
		Model:   "openrouter/free",
		BaseURL: srv.URL,
	}
	p := NewOpenRouterProvider(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sr, err := p.ChatStream(ctx, ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	events := collectEvents(t, sr)

	// Should have received some events but not all 100.
	if len(events) >= 100 {
		t.Errorf("got %d events, expected fewer due to context cancellation", len(events))
	}
	// Should complete quickly (not hang).
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_EmptyChoices — chunks with no choices are skipped
// --------------------------------------------------------------------------

func TestOpenRouterStream_EmptyChoices(t *testing.T) {
	sse := sseData(`{"choices":[]}`) +
		sseData(`{"choices":[{"delta":{"content":"ok"},"finish_reason":null}]}`) +
		sseData(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`) +
		sseDone()

	_, cfg := makeStreamServer(t, sse)
	p := NewOpenRouterProvider(cfg)

	sr, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want ok", resp.Content)
	}
}

// --------------------------------------------------------------------------
// TestOpenRouterStream_InterfaceAssertion — compile-time check
// --------------------------------------------------------------------------

func TestOpenRouterStream_InterfaceAssertion(t *testing.T) {
	var _ StreamingProvider = (*OpenRouterProvider)(nil)
}
