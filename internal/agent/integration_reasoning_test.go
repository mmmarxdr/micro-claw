package agent

// integration_reasoning_test.go — Integration test for the reasoning (thinking) stream flow.
//
// Verifies end-to-end: a fake StreamingProvider emits ReasoningDelta + TextDelta events
// through processStreamingCall, and the stream writer receives them in the correct order.
//
// Note: reasoning-only finalization (no leak) is already covered in stream_test.go by
// TestProcessStreamingCall_ReasoningOnly_FinalizesWriter — referenced here to avoid duplication.

import (
	"context"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/skill"
)

// TestIntegration_ReasoningFlow verifies the full reasoning → text stream pipeline:
//   - ReasoningDelta events reach the writer as WriteReasoning calls (in order)
//   - TextDelta events reach the writer as WriteChunk calls (in order)
//   - Reasoning text is NOT included in the assembled ChatResponse.Content
//   - Writer is Finalized (not leaked)
func TestIntegration_ReasoningFlow(t *testing.T) {
	// Script: think1, think2, then answer text, then done.
	events := []provider.StreamEvent{
		{Type: provider.StreamEventReasoningDelta, Text: "thinking..."},
		{Type: provider.StreamEventReasoningDelta, Text: " more thinking"},
		{Type: provider.StreamEventTextDelta, Text: "answer"},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{
		Content:    "answer",
		StopReason: "end_turn",
	}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	resp, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "integration-test", 0, time.Now(), nil,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !textStreamed {
		t.Error("expected textStreamed=true")
	}

	// Assert assembled content contains only the text, NOT reasoning.
	if resp.Content != "answer" {
		t.Errorf("expected Content='answer' (no reasoning mixed in), got %q", resp.Content)
	}

	w := sCh.writer
	if w == nil {
		t.Fatal("expected stream writer to be opened")
	}

	// Assert reasoning arrived before text (WriteReasoning called first).
	reasoning := w.getReasoning()
	if len(reasoning) != 2 {
		t.Fatalf("expected 2 WriteReasoning calls, got %d: %v", len(reasoning), reasoning)
	}
	if reasoning[0] != "thinking..." {
		t.Errorf("expected reasoning[0]='thinking...', got %q", reasoning[0])
	}
	if reasoning[1] != " more thinking" {
		t.Errorf("expected reasoning[1]=' more thinking', got %q", reasoning[1])
	}

	// Assert text chunks.
	chunks := w.getChunks()
	if len(chunks) != 1 || chunks[0] != "answer" {
		t.Errorf("expected 1 text chunk 'answer', got %v", chunks)
	}

	// Writer must be Finalized (no stream leak).
	if !w.finalized {
		t.Error("expected Finalize() to be called — writer would be leaked otherwise")
	}
	if w.aborted {
		t.Error("did not expect Abort() to be called")
	}
}

// TestIntegration_ReasoningOnly_WriterNotLeaked verifies that a reasoning-only stream
// (no TextDelta) still opens and properly finalizes the writer.
// See also: TestProcessStreamingCall_ReasoningOnly_FinalizesWriter in stream_test.go (unit coverage).
func TestIntegration_ReasoningOnly_WriterNotLeaked(t *testing.T) {
	events := []provider.StreamEvent{
		{Type: provider.StreamEventReasoningDelta, Text: "internal thinking only"},
		{Type: provider.StreamEventDone},
	}
	assembledResp := &provider.ChatResponse{Content: "", StopReason: "end_turn"}

	sp := &mockStreamingProvider{
		streamFunc: func(ctx context.Context, req provider.ChatRequest) (*provider.StreamResult, error) {
			return scriptedStream(events, assembledResp, nil), nil
		},
	}
	sCh := &mockStreamChannel{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, sCh, sp, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, true)

	_, textStreamed, err := ag.processStreamingCall(
		context.Background(), sp, sCh, provider.ChatRequest{}, "integration-test", 0, time.Now(), nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textStreamed {
		t.Error("expected textStreamed=false for reasoning-only stream")
	}

	w := sCh.writer
	if w == nil {
		t.Fatal("expected writer to be opened for reasoning delivery")
	}
	if !w.finalized {
		t.Error("expected Finalize() to be called — reasoning-only writer leaked")
	}

	reasoning := w.getReasoning()
	if len(reasoning) != 1 || reasoning[0] != "internal thinking only" {
		t.Errorf("expected reasoning=['internal thinking only'], got %v", reasoning)
	}
}

// See integration_test.go for the noopAuditor (audit.NoopAuditor) and other helpers.
