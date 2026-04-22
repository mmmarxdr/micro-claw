package agent

// M9: ragSearchWithHyDE with injected RingRecorder — Snapshot contains one Event.
// M10: ragSearchWithHyDE with ragMetrics == nil — no panic (NoopRecorder fallback).

import (
	"context"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
	"daimon/internal/skill"
)

// ---------------------------------------------------------------------------
// M9: ragSearchWithHyDE with injected RingRecorder — one Event, fields set.
// ---------------------------------------------------------------------------

func TestLoopMetrics_HyDE_RecordsEvent(t *testing.T) {
	t.Parallel()
	store := &hydeDocStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "chunk-1", Content: "content"}, DocTitle: "Doc", Score: 1.0},
		},
	}

	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	ag := New(cfg, defaultLimits(), config.FilterConfig{}, &mockChannel{}, prov, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	embedFn := func(_ context.Context, text string) ([]float32, error) {
		v := float32(len(text)%5+1) / 5.0
		return []float32{v, 0.5, 0.5}, nil
	}
	ag.WithRAGStore(store, embedFn, 5, 1000)

	rec := metrics.NewRingRecorder(10)
	ag.WithRAGMetrics(rec)

	hypoFn := func(_ context.Context, q string) (string, error) {
		return "a canned hypothesis for: " + q, nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	runTurn(t, ag, "test query for metrics")

	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("M9: expected at least one event in recorder snapshot")
	}
	ev := snap[len(snap)-1]
	if !ev.HydeEnabled {
		t.Error("M9: event.HydeEnabled should be true for successful HyDE turn")
	}
	if ev.FinalChunksReturned == 0 {
		t.Error("M9: event.FinalChunksReturned should be > 0")
	}
	if ev.ProvenanceBreakdown == nil {
		t.Error("M9: event.ProvenanceBreakdown should be non-nil")
	}
	if ev.TotalDurationMs < 0 {
		t.Errorf("M9: event.TotalDurationMs should be >= 0, got %d", ev.TotalDurationMs)
	}
}

// ---------------------------------------------------------------------------
// M10: ragMetrics == nil — no panic, retrieval still works.
// ---------------------------------------------------------------------------

func TestLoopMetrics_NilRecorder_NoPanic(t *testing.T) {
	t.Parallel()
	store := &hydeDocStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "content"}, DocTitle: "D", Score: 1.0},
		},
	}

	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	ag := hydeAgent(t, prov, store)
	// Do NOT call WithRAGMetrics — ragMetrics stays nil.

	hypoFn := func(_ context.Context, q string) (string, error) {
		return "hypothesis: " + q, nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	// Should not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("M10: panicked with ragMetrics==nil: %v", r)
		}
	}()
	runTurn(t, ag, "query without recorder")

	// Retrieval should still work.
	if store.callCount() < 1 {
		t.Error("M10: expected at least 1 SearchChunks call even without metrics recorder")
	}
}
