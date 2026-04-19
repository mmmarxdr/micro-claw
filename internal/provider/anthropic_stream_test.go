package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/content"
)

// --------------------------------------------------------------------------
// helpers — build mock SSE payloads for Anthropic streaming
// --------------------------------------------------------------------------

// buildAnthropicSSE constructs a complete SSE stream from typed events.
func buildAnthropicSSE(frames []string) string {
	return strings.Join(frames, "")
}

func sseFrame(event, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
}

func messageStartFrame(inputTokens int) string {
	return sseFrame("message_start", fmt.Sprintf(
		`{"type":"message_start","message":{"id":"msg_test","model":"claude-3-5-sonnet","usage":{"input_tokens":%d}}}`,
		inputTokens,
	))
}

func textBlockStartFrame(index int) string {
	return sseFrame("content_block_start", fmt.Sprintf(
		`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`,
		index,
	))
}

func textDeltaFrame(index int, text string) string {
	escaped, _ := json.Marshal(text) // properly escape the text
	return sseFrame("content_block_delta", fmt.Sprintf(
		`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`,
		index, string(escaped),
	))
}

func toolBlockStartFrame(index int, id, name string) string {
	return sseFrame("content_block_start", fmt.Sprintf(
		`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"%s","name":"%s"}}`,
		index, id, name,
	))
}

func toolDeltaFrame(index int, partialJSON string) string {
	escaped, _ := json.Marshal(partialJSON)
	return sseFrame("content_block_delta", fmt.Sprintf(
		`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`,
		index, string(escaped),
	))
}

func blockStopFrame(index int) string {
	return sseFrame("content_block_stop", fmt.Sprintf(
		`{"type":"content_block_stop","index":%d}`, index,
	))
}

func messageDeltaFrame(stopReason string, outputTokens int) string {
	return sseFrame("message_delta", fmt.Sprintf(
		`{"type":"message_delta","delta":{"stop_reason":"%s"},"usage":{"output_tokens":%d}}`,
		stopReason, outputTokens,
	))
}

func messageStopFrame() string {
	return sseFrame("message_stop", `{"type":"message_stop"}`)
}

func errorFrame(errType, errMsg string) string {
	return sseFrame("error", fmt.Sprintf(
		`{"type":"error","error":{"type":"%s","message":"%s"}}`, errType, errMsg,
	))
}

// newStreamTestProvider creates an AnthropicProvider pointed at the test server.
func newStreamTestProvider(t *testing.T, ts *httptest.Server) *AnthropicProvider {
	t.Helper()
	return NewAnthropicProvider(config.ProviderConfig{
		APIKey:  "test-key",
		BaseURL: ts.URL,
	})
}

// --------------------------------------------------------------------------
// Interface compliance
// --------------------------------------------------------------------------

func TestAnthropicProvider_ImplementsStreamingProvider(t *testing.T) {
	var _ StreamingProvider = (*AnthropicProvider)(nil)
}

// --------------------------------------------------------------------------
// TestAnthropicStream_TextOnly — text-only streaming response
// --------------------------------------------------------------------------

func TestAnthropicStream_TextOnly(t *testing.T) {
	ssePayload := buildAnthropicSSE([]string{
		messageStartFrame(25),
		textBlockStartFrame(0),
		textDeltaFrame(0, "Hello"),
		textDeltaFrame(0, " world"),
		textDeltaFrame(0, "!"),
		blockStopFrame(0),
		messageDeltaFrame("end_turn", 15),
		messageStopFrame(),
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var textChunks []string
	var gotUsage bool
	var gotDone bool

	for ev := range sr.Events {
		switch ev.Type {
		case StreamEventTextDelta:
			textChunks = append(textChunks, ev.Text)
		case StreamEventUsage:
			gotUsage = true
			if ev.Usage.InputTokens != 25 {
				t.Errorf("expected InputTokens 25, got %d", ev.Usage.InputTokens)
			}
			if ev.Usage.OutputTokens != 15 {
				t.Errorf("expected OutputTokens 15, got %d", ev.Usage.OutputTokens)
			}
			if ev.StopReason != "end_turn" {
				t.Errorf("expected StopReason 'end_turn', got %q", ev.StopReason)
			}
		case StreamEventDone:
			gotDone = true
		case StreamEventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}

	if len(textChunks) != 3 {
		t.Fatalf("expected 3 text delta events, got %d: %v", len(textChunks), textChunks)
	}
	if strings.Join(textChunks, "") != "Hello world!" {
		t.Errorf("assembled text = %q, want 'Hello world!'", strings.Join(textChunks, ""))
	}
	if !gotUsage {
		t.Error("expected Usage event")
	}
	if !gotDone {
		t.Error("expected Done event")
	}

	// Verify assembled response.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Hello world!" {
		t.Errorf("Response Content = %q, want 'Hello world!'", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("Response StopReason = %q, want 'end_turn'", resp.StopReason)
	}
	if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 15 {
		t.Errorf("Response Usage = %+v, want {25, 15}", resp.Usage)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_ToolCall — tool call streaming response
// --------------------------------------------------------------------------

func TestAnthropicStream_ToolCall(t *testing.T) {
	ssePayload := buildAnthropicSSE([]string{
		messageStartFrame(30),
		toolBlockStartFrame(0, "toolu_123", "shell_exec"),
		toolDeltaFrame(0, `{"command":`),
		toolDeltaFrame(0, `"ls -la"}`),
		blockStopFrame(0),
		messageDeltaFrame("tool_use", 20),
		messageStopFrame(),
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var eventTypes []StreamEventType
	for ev := range sr.Events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Expected sequence: ToolCallStart, ToolCallDelta, ToolCallDelta, ToolCallEnd, Usage, Done
	expected := []StreamEventType{
		StreamEventToolCallStart,
		StreamEventToolCallDelta,
		StreamEventToolCallDelta,
		StreamEventToolCallEnd,
		StreamEventUsage,
		StreamEventDone,
	}
	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, et := range expected {
		if eventTypes[i] != et {
			t.Errorf("event[%d]: expected %v, got %v", i, et, eventTypes[i])
		}
	}

	// Verify assembled response.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "" {
		t.Errorf("expected empty Content, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_123" {
		t.Errorf("ToolCall.ID = %q, want 'toolu_123'", tc.ID)
	}
	if tc.Name != "shell_exec" {
		t.Errorf("ToolCall.Name = %q, want 'shell_exec'", tc.Name)
	}
	var input map[string]any
	if err := json.Unmarshal(tc.Input, &input); err != nil {
		t.Fatalf("ToolCall.Input is not valid JSON: %v", err)
	}
	if input["command"] != "ls -la" {
		t.Errorf("ToolCall.Input.command = %v, want 'ls -la'", input["command"])
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_MixedContent — text followed by tool call
// --------------------------------------------------------------------------

func TestAnthropicStream_MixedContent(t *testing.T) {
	ssePayload := buildAnthropicSSE([]string{
		messageStartFrame(40),
		// Text block
		textBlockStartFrame(0),
		textDeltaFrame(0, "Let me check that for you."),
		blockStopFrame(0),
		// Tool use block
		toolBlockStartFrame(1, "toolu_456", "search"),
		toolDeltaFrame(1, `{"query":"test"}`),
		blockStopFrame(1),
		messageDeltaFrame("tool_use", 35),
		messageStopFrame(),
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var eventTypes []StreamEventType
	for ev := range sr.Events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Expected: TextDelta, ToolCallStart, ToolCallDelta, ToolCallEnd, Usage, Done
	expected := []StreamEventType{
		StreamEventTextDelta,
		StreamEventToolCallStart,
		StreamEventToolCallDelta,
		StreamEventToolCallEnd,
		StreamEventUsage,
		StreamEventDone,
	}
	if len(eventTypes) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(eventTypes), eventTypes)
	}
	for i, et := range expected {
		if eventTypes[i] != et {
			t.Errorf("event[%d]: expected %v, got %v", i, et, eventTypes[i])
		}
	}

	// Verify assembled response.
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "Let me check that for you." {
		t.Errorf("Response Content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("ToolCall.Name = %q, want 'search'", resp.ToolCalls[0].Name)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want 'tool_use'", resp.StopReason)
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_ErrorEvent — error event mid-stream
// --------------------------------------------------------------------------

func TestAnthropicStream_ErrorEvent(t *testing.T) {
	ssePayload := buildAnthropicSSE([]string{
		messageStartFrame(10),
		textBlockStartFrame(0),
		textDeltaFrame(0, "partial"),
		errorFrame("server_error", "internal failure"),
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var gotText bool
	var gotError bool
	for ev := range sr.Events {
		switch ev.Type {
		case StreamEventTextDelta:
			gotText = true
		case StreamEventError:
			gotError = true
			if !strings.Contains(ev.Err.Error(), "internal failure") {
				t.Errorf("error message = %q, want it to contain 'internal failure'", ev.Err.Error())
			}
		}
	}

	if !gotText {
		t.Error("expected text delta before error")
	}
	if !gotError {
		t.Error("expected error event")
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_HTTPError — non-200 response returns error immediately
// --------------------------------------------------------------------------

func TestAnthropicStream_HTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "rate limit 429",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"message":"rate limited"}}`,
			wantErr:    ErrRateLimit,
		},
		{
			name:       "server error 500",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"message":"server error"}}`,
			wantErr:    ErrUnavailable,
		},
		{
			name:       "unauthorized 401",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"message":"invalid key"}}`,
			wantErr:    ErrAuth,
		},
		{
			name:       "bad request 400",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"message":"bad param"}}`,
			wantErr:    ErrBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			prov := newStreamTestProvider(t, ts)
			_, err := prov.ChatStream(context.Background(), minimalRequest())
			if err == nil {
				t.Fatal("expected error from HTTP error response")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_RequestContainsStreamTrue — verify stream:true in body
// --------------------------------------------------------------------------

func TestAnthropicStream_RequestContainsStreamTrue(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		// Return a minimal valid SSE stream.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload := buildAnthropicSSE([]string{
			messageStartFrame(5),
			messageDeltaFrame("end_turn", 1),
			messageStopFrame(),
		})
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	// Drain events.
	for range sr.Events {
	}
	_, _ = sr.Response()

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("could not unmarshal request body: %v\nbody: %s", err, capturedBody)
	}
	stream, ok := body["stream"].(bool)
	if !ok || !stream {
		t.Errorf("expected stream=true in request body, got %v", body["stream"])
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_RequiredHeaders — verify API headers are set
// --------------------------------------------------------------------------

func TestAnthropicStream_RequiredHeaders(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload := buildAnthropicSSE([]string{
			messageStartFrame(5),
			messageDeltaFrame("end_turn", 1),
			messageStopFrame(),
		})
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}
	for range sr.Events {
	}
	_, _ = sr.Response()

	if capturedReq == nil {
		t.Fatal("server never received a request")
	}
	if got := capturedReq.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q, want 'test-key'", got)
	}
	if got := capturedReq.Header.Get("anthropic-version"); got == "" {
		t.Error("anthropic-version header not set")
	}
	if got := capturedReq.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q, want 'application/json'", got)
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_MultipleToolCalls — two tool calls in one message
// --------------------------------------------------------------------------

func TestAnthropicStream_MultipleToolCalls(t *testing.T) {
	ssePayload := buildAnthropicSSE([]string{
		messageStartFrame(50),
		toolBlockStartFrame(0, "toolu_1", "tool_a"),
		toolDeltaFrame(0, `{"arg":"val1"}`),
		blockStopFrame(0),
		toolBlockStartFrame(1, "toolu_2", "tool_b"),
		toolDeltaFrame(1, `{"arg":"val2"}`),
		blockStopFrame(1),
		messageDeltaFrame("tool_use", 30),
		messageStopFrame(),
	})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	for range sr.Events {
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_1" || resp.ToolCalls[0].Name != "tool_a" {
		t.Errorf("tool call 0: %+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "toolu_2" || resp.ToolCalls[1].Name != "tool_b" {
		t.Errorf("tool call 1: %+v", resp.ToolCalls[1])
	}
}

// --------------------------------------------------------------------------
// TestAnthropicStream_ContextCancellation — context cancel during stream
// --------------------------------------------------------------------------

func TestAnthropicStream_ContextCancellation(t *testing.T) {
	// Server sends SSE slowly — one event at a time with delays.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Send message_start, then block.
		_, _ = w.Write([]byte(messageStartFrame(5)))
		flusher.Flush()

		// Block until client disconnects (context cancelled).
		<-r.Context().Done()
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sr, err := prov.ChatStream(ctx, minimalRequest())
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	// Drain events — should complete quickly after context cancel.
	start := time.Now()
	for range sr.Events {
	}
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("event draining took %v, expected <2s after context cancel", elapsed)
	}

	// Response should return an error (stream interrupted).
	_, _ = sr.Response()
	// The goroutine may complete with a parse error from the broken connection,
	// or it may have no events after message_start and return a partial response.
	// Either way, it should not hang.
}

// --------------------------------------------------------------------------
// TestAnthropicStream_BuildsRequestLikeChat — verify request body matches Chat()
// --------------------------------------------------------------------------

func TestAnthropicStream_BuildsRequestLikeChat(t *testing.T) {
	var capturedBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = readAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload := buildAnthropicSSE([]string{
			messageStartFrame(5),
			messageDeltaFrame("end_turn", 1),
			messageStopFrame(),
		})
		_, _ = w.Write([]byte(payload))
	}))
	defer ts.Close()

	prov := newStreamTestProvider(t, ts)
	req := ChatRequest{
		SystemPrompt: "You are helpful.",
		Messages: []ChatMessage{
			{Role: "user", Content: content.TextBlock("hi")},
			{
				Role:    "assistant",
				Content: content.TextBlock("I'll help"),
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "shell_exec", Input: json.RawMessage(`{"cmd":"ls"}`)},
				},
			},
			{Role: "tool", Content: content.TextBlock("file1.txt"), ToolCallID: "tc1"},
		},
		Tools: []ToolDefinition{
			{
				Name:        "shell_exec",
				Description: "Run shell",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		MaxTokens: 8192,
	}

	sr, err := prov.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}
	for range sr.Events {
	}
	_, _ = sr.Response()

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal error: %v\nbody: %s", err, capturedBody)
	}

	// Verify key fields.
	if body["system"] != "You are helpful." {
		t.Errorf("system = %v", body["system"])
	}
	if body["max_tokens"].(float64) != 8192 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}

	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}

	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
}
