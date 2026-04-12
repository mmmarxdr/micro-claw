package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/provider"
	"microagent/internal/skill"
	"microagent/internal/tool"
)

// ---------------------------------------------------------------------------
// Mock types for streaming tests
// ---------------------------------------------------------------------------

// mockStreamingProvider implements both Provider and StreamingProvider.
type mockStreamingProvider struct {
	mockProvider // embeds sync mock
	streamFunc   func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error)
}

func (m *mockStreamingProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, req)
	}
	return nil, errors.New("ChatStream not implemented")
}

// mockStreamWriter captures chunks for assertion.
type mockStreamWriter struct {
	mu        sync.Mutex
	chunks    []string
	finalized bool
	aborted   bool
	abortErr  error
}

func (w *mockStreamWriter) WriteChunk(text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.chunks = append(w.chunks, text)
	return nil
}

func (w *mockStreamWriter) Finalize() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.finalized = true
	return nil
}

func (w *mockStreamWriter) Abort(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.aborted = true
	w.abortErr = err
	return nil
}

func (w *mockStreamWriter) getChunks() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]string, len(w.chunks))
	copy(cp, w.chunks)
	return cp
}

// mockStreamChannel implements both Channel and StreamSender.
type mockStreamChannel struct {
	mockChannel
	writer *mockStreamWriter
}

func (m *mockStreamChannel) BeginStream(ctx context.Context, channelID string) (channel.StreamWriter, error) {
	m.writer = &mockStreamWriter{}
	return m.writer, nil
}

// scriptedStream creates a StreamResult from a list of events and a final response.
func scriptedStream(events []provider.StreamEvent, resp *provider.ChatResponse, respErr error) *provider.StreamResult {
	sr, ch := provider.NewStreamResult(len(events) + 1)
	go func() {
		defer close(ch)
		for _, ev := range events {
			ch <- ev
		}
		sr.SetResponse(resp, respErr)
	}()
	return sr
}

// ---------------------------------------------------------------------------
// Test: text-only streaming with StreamWriter
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_TextOnly_WithWriter(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamEventTextDelta, Text: "Hello"},
		{Type: provider.StreamEventTextDelta, Text: " world"},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{
		Content:    "Hello world",
		StopReason: "end_turn",
		Usage:      provider.UsageStats{InputTokens: 10, OutputTokens: 5},
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	resp, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !textStreamed {
		t.Error("expected textStreamed=true")
	}
	if resp.Content != "Hello world" {
		t.Errorf("expected Content='Hello world', got %q", resp.Content)
	}

	// Verify stream writer received chunks.
	chunks := sCh.writer.getChunks()
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "Hello" || chunks[1] != " world" {
		t.Errorf("unexpected chunks: %v", chunks)
	}
	if !sCh.writer.finalized {
		t.Error("expected Finalize to be called")
	}
	if sCh.writer.aborted {
		t.Error("did not expect Abort to be called")
	}
}

// ---------------------------------------------------------------------------
// Test: text-only streaming without StreamSender (fallback to buffered)
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_TextOnly_WithoutWriter(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamEventTextDelta, Text: "buffered text"},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{
		Content:    "buffered text",
		StopReason: "end_turn",
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}

	// Use a plain mockChannel that does NOT implement StreamSender.
	ch := &mockChannel{}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	resp, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, nil, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textStreamed {
		t.Error("expected textStreamed=false when no StreamSender")
	}
	if resp.Content != "buffered text" {
		t.Errorf("expected Content='buffered text', got %q", resp.Content)
	}
}

// ---------------------------------------------------------------------------
// Test: tool-only response (no text deltas, no stream writer opened)
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_ToolOnly(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamEventToolCallStart, ToolCallID: "tc1", ToolName: "shell"},
		{Type: provider.StreamEventToolCallDelta, ToolInput: `{"cmd":"ls"}`},
		{Type: provider.StreamEventToolCallEnd},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "shell", Input: json.RawMessage(`{"cmd":"ls"}`)},
		},
		StopReason: "tool_use",
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	resp, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textStreamed {
		t.Error("expected textStreamed=false for tool-only response")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "shell" {
		t.Errorf("expected tool name 'shell', got %q", resp.ToolCalls[0].Name)
	}

	// BeginStream should NOT have been called (writer should be nil).
	if sCh.writer != nil {
		t.Error("expected BeginStream to NOT be called for tool-only response")
	}
}

// ---------------------------------------------------------------------------
// Test: mixed content (text + tool calls)
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_TextThenTools(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamEventTextDelta, Text: "Let me check..."},
		{Type: provider.StreamEventToolCallStart, ToolCallID: "tc1", ToolName: "shell"},
		{Type: provider.StreamEventToolCallDelta, ToolInput: `{"cmd":"ls"}`},
		{Type: provider.StreamEventToolCallEnd},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{
		Content: "Let me check...",
		ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "shell", Input: json.RawMessage(`{"cmd":"ls"}`)},
		},
		StopReason: "tool_use",
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	resp, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !textStreamed {
		t.Error("expected textStreamed=true")
	}
	if resp.Content != "Let me check..." {
		t.Errorf("expected Content='Let me check...', got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	// Writer should have received text.
	chunks := sCh.writer.getChunks()
	if len(chunks) != 1 || chunks[0] != "Let me check..." {
		t.Errorf("unexpected chunks: %v", chunks)
	}
	if !sCh.writer.finalized {
		t.Error("expected Finalize to be called")
	}
}

// ---------------------------------------------------------------------------
// Test: mid-stream error
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_Error(t *testing.T) {
	streamErr := errors.New("connection reset")
	events := []provider.StreamEvent{
		{Type: provider.StreamEventTextDelta, Text: "partial"},
		{Type: provider.StreamEventError, Err: streamErr},
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, nil, streamErr), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	_, _, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if err == nil {
		t.Fatal("expected error from processStreamingCall")
	}
	if !errors.Is(err, streamErr) {
		t.Errorf("expected streamErr, got %v", err)
	}

	// Writer should have been aborted.
	if sCh.writer == nil {
		t.Fatal("expected writer to have been created")
	}
	if !sCh.writer.aborted {
		t.Error("expected Abort to be called on writer")
	}
}

// ---------------------------------------------------------------------------
// Test: pre-stream error (ChatStream itself fails)
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_PreStreamError(t *testing.T) {
	preErr := errors.New("auth failed")
	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return nil, preErr
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	_, _, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
	)

	if !errors.Is(err, preErr) {
		t.Errorf("expected preErr, got %v", err)
	}
	// No writer should have been opened.
	if sCh.writer != nil {
		t.Error("expected no writer to be created on pre-stream error")
	}
}

// ---------------------------------------------------------------------------
// Test: context cancellation mid-stream
// ---------------------------------------------------------------------------

func TestProcessStreamingCall_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Stream that blocks until context is cancelled.
	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			sr, ch := provider.NewStreamResult(8)
			go func() {
				defer close(ch)
				ch <- provider.StreamEvent{Type: provider.StreamEventTextDelta, Text: "start"}
				// Block until context cancelled.
				<-ctx.Done()
				ch <- provider.StreamEvent{Type: provider.StreamEventError, Err: ctx.Err()}
				sr.SetResponse(nil, ctx.Err())
			}()
			return sr, nil
		},
	}
	sCh := &mockStreamChannel{}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, err := ag.processStreamingCall(
			ctx, sp, sCh, provider.ChatRequest{}, "test", 0, time.Now(), nil,
		)
		if err == nil {
			t.Error("expected error after context cancel")
		}
	}()

	// Cancel after a short delay to let the first text delta through.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good — goroutine exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("processStreamingCall did not return after context cancel")
	}
}

// ---------------------------------------------------------------------------
// Test: full processMessage with streaming enabled (text response)
// ---------------------------------------------------------------------------

func TestProcessMessage_StreamEnabled_TextOnly(t *testing.T) {
	assembledResp := &provider.ChatResponse{
		Content:    "streamed answer",
		StopReason: "end_turn",
		Usage:      provider.UsageStats{InputTokens: 10, OutputTokens: 5},
	}

	sp := &mockStreamingProvider{
		mockProvider: mockProvider{
			responses: []provider.ChatResponse{*assembledResp},
		},
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream([]provider.StreamEvent{
				{Type: provider.StreamEventTextDelta, Text: "streamed "},
				{Type: provider.StreamEventTextDelta, Text: "answer"},
				{Type: provider.StreamEventUsage, Usage: &assembledResp.Usage, StopReason: "end_turn"},
				{Type: provider.StreamEventDone},
			}, assembledResp, nil), nil
		},
	}

	sCh := &mockStreamChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// Text was streamed, so channel.Send() should NOT have been called with the response text.
	for _, msg := range sCh.sent {
		if msg.Text == "streamed answer" {
			t.Error("channel.Send() should not be called for streamed text")
		}
	}

	// Verify writer received chunks.
	if sCh.writer == nil {
		t.Fatal("expected stream writer to be created")
	}
	chunks := sCh.writer.getChunks()
	joined := strings.Join(chunks, "")
	if joined != "streamed answer" {
		t.Errorf("expected chunks to form 'streamed answer', got %q", joined)
	}

	// Conversation should be saved.
	if st.conv == nil {
		t.Fatal("expected conversation to be saved")
	}
}

// ---------------------------------------------------------------------------
// Test: full processMessage with streaming + tool calls (multi-iteration)
// ---------------------------------------------------------------------------

func TestProcessMessage_StreamEnabled_WithToolCalls(t *testing.T) {
	callCount := 0

	toolCallResp := &provider.ChatResponse{
		Content: "Let me check...",
		ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
		StopReason: "tool_use",
		Usage:      provider.UsageStats{InputTokens: 10, OutputTokens: 5},
	}
	finalResp := &provider.ChatResponse{
		Content:    "Done!",
		StopReason: "end_turn",
		Usage:      provider.UsageStats{InputTokens: 20, OutputTokens: 10},
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			callCount++
			if callCount == 1 {
				return scriptedStream([]provider.StreamEvent{
					{Type: provider.StreamEventTextDelta, Text: "Let me check..."},
					{Type: provider.StreamEventToolCallStart, ToolCallID: "tc1", ToolName: "mock_tool"},
					{Type: provider.StreamEventToolCallDelta, ToolInput: `{}`},
					{Type: provider.StreamEventToolCallEnd},
					{Type: provider.StreamEventDone},
				}, toolCallResp, nil), nil
			}
			return scriptedStream([]provider.StreamEvent{
				{Type: provider.StreamEventTextDelta, Text: "Done!"},
				{Type: provider.StreamEventDone},
			}, finalResp, nil), nil
		},
	}

	sCh := &mockStreamChannel{}
	st := &mockStore{}
	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "tool result"}}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, st, audit.NoopAuditor{},
		map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, true)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("do something")})

	// Two streaming calls should have been made.
	if callCount != 2 {
		t.Errorf("expected 2 streaming calls, got %d", callCount)
	}

	// Tool should have been executed.
	if mt.calls != 1 {
		t.Errorf("expected tool to be called once, got %d", mt.calls)
	}

	// Conversation should be saved.
	if st.conv == nil {
		t.Fatal("expected conversation to be saved")
	}
}

// ---------------------------------------------------------------------------
// Test: stream=true but provider doesn't implement StreamingProvider
// ---------------------------------------------------------------------------

func TestProcessMessage_StreamFallbackToSync(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "sync response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	// stream=true but mockProvider does NOT implement StreamingProvider.
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	// The constructor should have set stream=false since the provider doesn't support it.
	if ag.stream {
		t.Error("expected agent.stream to be false when provider doesn't implement StreamingProvider")
	}

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// Should fall back to sync path and deliver via Send().
	if len(ch.sent) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(ch.sent))
	}
	if ch.sent[0].Text != "sync response" {
		t.Errorf("expected 'sync response', got %q", ch.sent[0].Text)
	}
}
