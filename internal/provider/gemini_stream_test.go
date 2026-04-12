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

	"microagent/internal/content"
)

// --------------------------------------------------------------------------
// helpers — build mock SSE payloads for Gemini streaming
// --------------------------------------------------------------------------

// geminiSSEFrame builds a single SSE data frame from a JSON string.
func geminiSSEFrame(data string) string {
	return fmt.Sprintf("data: %s\n\n", data)
}

// geminiTextChunk builds a Gemini SSE chunk containing a text part.
func geminiTextChunk(text string, promptTokens, candidateTokens int) string {
	escaped, _ := json.Marshal(text)
	return geminiSSEFrame(fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"text":%s}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}}`,
		string(escaped), promptTokens, candidateTokens,
	))
}

// geminiTextChunkWithStop builds a Gemini SSE chunk with text and a finishReason.
func geminiTextChunkWithStop(text string, finishReason string, promptTokens, candidateTokens int) string {
	escaped, _ := json.Marshal(text)
	return geminiSSEFrame(fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"text":%s}],"role":"model"},"index":0,"finishReason":"%s"}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}}`,
		string(escaped), finishReason, promptTokens, candidateTokens,
	))
}

// geminiFuncCallChunk builds a Gemini SSE chunk containing a functionCall part with finishReason.
func geminiFuncCallChunk(name string, args map[string]any, finishReason string, promptTokens, candidateTokens int) string {
	argsBytes, _ := json.Marshal(args)
	return geminiSSEFrame(fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"%s","args":%s}}],"role":"model"},"index":0,"finishReason":"%s"}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}}`,
		name, string(argsBytes), finishReason, promptTokens, candidateTokens,
	))
}

// geminiMixedChunk builds a Gemini SSE chunk containing both text and functionCall parts with finishReason.
func geminiMixedChunk(text, funcName string, funcArgs map[string]any, finishReason string, promptTokens, candidateTokens int) string {
	escaped, _ := json.Marshal(text)
	argsBytes, _ := json.Marshal(funcArgs)
	return geminiSSEFrame(fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[{"text":%s},{"functionCall":{"name":"%s","args":%s}}],"role":"model"},"index":0,"finishReason":"%s"}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}}`,
		string(escaped), funcName, string(argsBytes), finishReason, promptTokens, candidateTokens,
	))
}

// newGeminiStreamTestProvider creates a GeminiProvider pointed at the test server.
func newGeminiStreamTestProvider(t *testing.T, ts *httptest.Server) *GeminiProvider {
	t.Helper()
	return newTestGeminiProvider(ts.URL)
}

// --------------------------------------------------------------------------
// Interface compliance
// --------------------------------------------------------------------------

func TestGeminiProvider_ImplementsStreamingProvider(t *testing.T) {
	var _ StreamingProvider = (*GeminiProvider)(nil)
}

// --------------------------------------------------------------------------
// TestGeminiStream_TextOnly — text-only streaming response
// --------------------------------------------------------------------------

func TestGeminiStream_TextOnly(t *testing.T) {
	ssePayload := strings.Join([]string{
		geminiTextChunk("Hello", 25, 5),
		geminiTextChunk(" world", 25, 10),
		geminiTextChunkWithStop("!", "STOP", 25, 12),
	}, "")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify streaming endpoint is used.
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("expected streamGenerateContent in path, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("expected alt=sse query param, got %q", r.URL.Query().Get("alt"))
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("expected key=test-key, got %q", r.URL.Query().Get("key"))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newGeminiStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), ChatRequest{
		SystemPrompt: "You are helpful.",
		Messages:     []ChatMessage{{Role: "user", Content: content.TextBlock("Hi!")}},
	})
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
			if ev.Usage.OutputTokens != 12 {
				t.Errorf("expected OutputTokens 12, got %d", ev.Usage.OutputTokens)
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
	if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 12 {
		t.Errorf("Response Usage = %+v, want {25, 12}", resp.Usage)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
}

// --------------------------------------------------------------------------
// TestGeminiStream_FunctionCall — function call streaming response
// --------------------------------------------------------------------------

func TestGeminiStream_FunctionCall(t *testing.T) {
	ssePayload := geminiFuncCallChunk("shell_exec", map[string]any{"command": "ls -la"}, "STOP", 20, 42)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newGeminiStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("List my files")}},
		Tools: []ToolDefinition{
			{Name: "shell_exec", Description: "Run shell command", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var eventTypes []StreamEventType
	for ev := range sr.Events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Gemini sends complete function calls: ToolCallStart, ToolCallDelta, ToolCallEnd, Usage, Done
	expected := []StreamEventType{
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
	if resp.Content != "" {
		t.Errorf("expected empty Content, got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_shell_exec" {
		t.Errorf("ToolCall.ID = %q, want 'call_shell_exec'", tc.ID)
	}
	if tc.Name != "shell_exec" {
		t.Errorf("ToolCall.Name = %q, want 'shell_exec'", tc.Name)
	}
	if resp.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want 'tool_use'", resp.StopReason)
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
// TestGeminiStream_MixedContent — text + function call in same chunk
// --------------------------------------------------------------------------

func TestGeminiStream_MixedContent(t *testing.T) {
	ssePayload := strings.Join([]string{
		geminiTextChunk("Let me check that for you.", 40, 10),
		geminiMixedChunk("", "search", map[string]any{"query": "test"}, "STOP", 40, 35),
	}, "")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newGeminiStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("Search for test")}},
		Tools: []ToolDefinition{
			{Name: "search", Description: "Search", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
		},
	})
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
// TestGeminiStream_HTTPError — non-200 response returns error immediately
// --------------------------------------------------------------------------

func TestGeminiStream_HTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{
			name:       "rate limit 429",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"rate limited"}}`,
			wantErr:    ErrRateLimit,
		},
		{
			name:       "server error 500",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"code":500,"message":"server error"}}`,
			wantErr:    ErrUnavailable,
		},
		{
			name:       "unauthorized 401",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"code":401,"message":"invalid key"}}`,
			wantErr:    ErrAuth,
		},
		{
			name:       "bad request 400",
			statusCode: http.StatusBadRequest,
			body:       `{"error":{"code":400,"message":"bad param"}}`,
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

			prov := newGeminiStreamTestProvider(t, ts)
			_, err := prov.ChatStream(context.Background(), ChatRequest{
				Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("test")}},
			})
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
// TestGeminiStream_EndpointURL — verify streaming endpoint is used
// --------------------------------------------------------------------------

func TestGeminiStream_EndpointURL(t *testing.T) {
	var capturedPath string
	var capturedQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(geminiTextChunkWithStop("ok", "STOP", 5, 1)))
	}))
	defer ts.Close()

	prov := newGeminiStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("test")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}
	for range sr.Events {
	}
	_, _ = sr.Response()

	if !strings.Contains(capturedPath, "streamGenerateContent") {
		t.Errorf("path = %q, want to contain 'streamGenerateContent'", capturedPath)
	}
	if !strings.Contains(capturedQuery, "alt=sse") {
		t.Errorf("query = %q, want to contain 'alt=sse'", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "key=test-key") {
		t.Errorf("query = %q, want to contain 'key=test-key'", capturedQuery)
	}
}

// --------------------------------------------------------------------------
// TestGeminiStream_MultipleTextChunks — streaming text across multiple chunks
// --------------------------------------------------------------------------

func TestGeminiStream_MultipleTextChunks(t *testing.T) {
	ssePayload := strings.Join([]string{
		geminiTextChunk("The ", 10, 2),
		geminiTextChunk("quick ", 10, 4),
		geminiTextChunk("brown ", 10, 6),
		geminiTextChunkWithStop("fox", "STOP", 10, 8),
	}, "")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer ts.Close()

	prov := newGeminiStreamTestProvider(t, ts)
	sr, err := prov.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: content.TextBlock("test")}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var textChunks []string
	for ev := range sr.Events {
		if ev.Type == StreamEventTextDelta {
			textChunks = append(textChunks, ev.Text)
		}
	}

	if len(textChunks) != 4 {
		t.Fatalf("expected 4 text chunks, got %d", len(textChunks))
	}

	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("Response() error: %v", err)
	}
	if resp.Content != "The quick brown fox" {
		t.Errorf("Response Content = %q, want 'The quick brown fox'", resp.Content)
	}
}
