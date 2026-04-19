package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --------------------------------------------------------------------------
// mockStreamingProvider — test double that implements StreamingProvider
// --------------------------------------------------------------------------

type mockStreamingProvider struct {
	mockProvider
	streamResult *StreamResult
	streamErr    error
	streamCalled int
}

func (m *mockStreamingProvider) ChatStream(_ context.Context, _ ChatRequest) (*StreamResult, error) {
	m.streamCalled++
	return m.streamResult, m.streamErr
}

// --------------------------------------------------------------------------
// Helper: drain a StreamResult and return the assembled response.
// --------------------------------------------------------------------------

func drainStream(t *testing.T, sr *StreamResult) *ChatResponse {
	t.Helper()
	resp, err := sr.Response()
	if err != nil {
		t.Fatalf("StreamResult.Response() error: %v", err)
	}
	return resp
}

// fakeStreamResult creates a minimal completed StreamResult for test assertions.
func fakeStreamResult(content string) *StreamResult {
	sr, events := NewStreamResult(8)
	go func() {
		defer close(events)
		if content != "" {
			events <- StreamEvent{Type: StreamEventTextDelta, Text: content}
		}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{Content: content}, nil)
	}()
	return sr
}

// --------------------------------------------------------------------------
// Compile-time assertion
// --------------------------------------------------------------------------

func TestFallbackProvider_ImplementsStreamingProvider(t *testing.T) {
	var _ StreamingProvider = (*FallbackProvider)(nil)
}

// --------------------------------------------------------------------------
// T1: Primary supports streaming, succeeds → delegates directly
// --------------------------------------------------------------------------

func TestFallbackStream_PrimaryStreaming_Succeeds(t *testing.T) {
	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamResult: fakeStreamResult("streamed-ok"),
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	sr, err := f.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	resp := drainStream(t, sr)
	if resp.Content != "streamed-ok" {
		t.Errorf("Content = %q, want streamed-ok", resp.Content)
	}
	if primary.streamCalled != 1 {
		t.Errorf("primary.streamCalled = %d, want 1", primary.streamCalled)
	}
	if fallback.chatCalled != 0 {
		t.Errorf("fallback.chatCalled = %d, want 0", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T2: Primary fails pre-stream (rate limit) → falls back to secondary streaming
// --------------------------------------------------------------------------

func TestFallbackStream_PrimaryFails_FallbackStreaming(t *testing.T) {
	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamErr:    fmt.Errorf("too many: %w", ErrRateLimit),
	}
	fallback := &mockStreamingProvider{
		mockProvider: mockProvider{name: "fallback"},
		streamResult: fakeStreamResult("fallback-streamed"),
	}
	f := newFallback(primary, fallback)

	sr, err := f.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	resp := drainStream(t, sr)
	if resp.Content != "fallback-streamed" {
		t.Errorf("Content = %q, want fallback-streamed", resp.Content)
	}
	if primary.streamCalled != 1 {
		t.Errorf("primary.streamCalled = %d, want 1", primary.streamCalled)
	}
	if fallback.streamCalled != 1 {
		t.Errorf("fallback.streamCalled = %d, want 1", fallback.streamCalled)
	}
}

// --------------------------------------------------------------------------
// T3: Primary fails pre-stream → falls back to secondary sync (syncToStream)
// --------------------------------------------------------------------------

func TestFallbackStream_PrimaryFails_FallbackSync(t *testing.T) {
	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamErr:    fmt.Errorf("server down: %w", ErrUnavailable),
	}
	// fallback is a plain Provider (no streaming)
	fallback := &mockProvider{
		name:     "fallback",
		chatResp: &ChatResponse{Content: "sync-fallback-ok"},
	}
	f := newFallback(primary, fallback)

	sr, err := f.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	resp := drainStream(t, sr)
	if resp.Content != "sync-fallback-ok" {
		t.Errorf("Content = %q, want sync-fallback-ok", resp.Content)
	}
	if primary.streamCalled != 1 {
		t.Errorf("primary.streamCalled = %d, want 1", primary.streamCalled)
	}
	if fallback.chatCalled != 1 {
		t.Errorf("fallback.chatCalled = %d, want 1", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T4: Neither supports streaming → syncToStream on primary
// --------------------------------------------------------------------------

func TestFallbackStream_NeitherStreaming_SyncToStreamPrimary(t *testing.T) {
	primary := &mockProvider{
		name:     "primary",
		chatResp: &ChatResponse{Content: "sync-primary"},
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	sr, err := f.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	resp := drainStream(t, sr)
	if resp.Content != "sync-primary" {
		t.Errorf("Content = %q, want sync-primary", resp.Content)
	}
	if primary.chatCalled != 1 {
		t.Errorf("primary.chatCalled = %d, want 1", primary.chatCalled)
	}
	if fallback.chatCalled != 0 {
		t.Errorf("fallback.chatCalled = %d, want 0", fallback.chatCalled)
	}
}

// --------------------------------------------------------------------------
// T5: Primary fails with non-eligible error → no fallback, error returned
// --------------------------------------------------------------------------

func TestFallbackStream_PrimaryAuthError_NoFallback(t *testing.T) {
	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamErr:    fmt.Errorf("bad key: %w", ErrAuth),
	}
	fallback := &mockStreamingProvider{
		mockProvider: mockProvider{name: "fallback"},
	}
	f := newFallback(primary, fallback)

	_, err := f.ChatStream(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("errors.Is(err, ErrAuth) = false; err = %v", err)
	}
	if fallback.streamCalled != 0 {
		t.Errorf("fallback.streamCalled = %d, want 0", fallback.streamCalled)
	}
}

// --------------------------------------------------------------------------
// Phase 3.4 — ReasoningDelta propagates through FallbackProvider (RS-4b)
// --------------------------------------------------------------------------

func TestFallbackStream_ReasoningDelta_PassesThrough(t *testing.T) {
	// Upstream emits a ReasoningDelta event; it must appear on the output channel unchanged.
	sr, events := NewStreamResult(8)
	go func() {
		defer close(events)
		events <- StreamEvent{Type: StreamEventReasoningDelta, Text: "thinking step"}
		events <- StreamEvent{Type: StreamEventTextDelta, Text: "answer"}
		events <- StreamEvent{Type: StreamEventDone}
		sr.SetResponse(&ChatResponse{Content: "answer"}, nil)
	}()

	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamResult: sr,
	}
	fallback := &mockProvider{name: "fallback"}
	f := newFallback(primary, fallback)

	result, err := f.ChatStream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream() error: %v", err)
	}

	var reasoningEvents []StreamEvent
	var textEvents []StreamEvent
	for ev := range result.Events {
		switch ev.Type {
		case StreamEventReasoningDelta:
			reasoningEvents = append(reasoningEvents, ev)
		case StreamEventTextDelta:
			textEvents = append(textEvents, ev)
		}
	}

	if len(reasoningEvents) != 1 {
		t.Fatalf("expected 1 ReasoningDelta event, got %d", len(reasoningEvents))
	}
	if reasoningEvents[0].Text != "thinking step" {
		t.Errorf("ReasoningDelta.Text = %q, want %q", reasoningEvents[0].Text, "thinking step")
	}
	if len(textEvents) != 1 {
		t.Errorf("expected 1 TextDelta, got %d", len(textEvents))
	}
}

// --------------------------------------------------------------------------
// T6: Both fail → combined error preserves primary sentinel
// --------------------------------------------------------------------------

func TestFallbackStream_BothFail(t *testing.T) {
	primary := &mockStreamingProvider{
		mockProvider: mockProvider{name: "primary"},
		streamErr:    fmt.Errorf("too many: %w", ErrRateLimit),
	}
	fallback := &mockStreamingProvider{
		mockProvider: mockProvider{name: "fallback"},
		streamErr:    fmt.Errorf("fallback also down"),
	}
	f := newFallback(primary, fallback)

	_, err := f.ChatStream(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRateLimit) {
		t.Errorf("errors.Is(err, ErrRateLimit) = false; err = %v", err)
	}
}
