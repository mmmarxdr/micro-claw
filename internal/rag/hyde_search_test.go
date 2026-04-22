package rag_test

// T1–T5: PerformHydeSearch unit tests.

import (
	"context"
	"errors"
	"testing"
	"time"

	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// hydeSearchStore tracks SearchChunks calls for PerformHydeSearch tests.
type hydeSearchStore struct {
	calls   []hydeSearchCall2
	results []rag.SearchResult
	err     error
}

type hydeSearchCall2 struct {
	query string
	vec   []float32
}

func (h *hydeSearchStore) AddDocument(_ context.Context, _ rag.Document) error { return nil }
func (h *hydeSearchStore) AddChunks(_ context.Context, _ string, _ []rag.DocumentChunk) error {
	return nil
}
func (h *hydeSearchStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (h *hydeSearchStore) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}
func (h *hydeSearchStore) GetDocument(_ context.Context, _ string) (rag.Document, error) {
	return rag.Document{}, rag.ErrDocNotFound
}
func (h *hydeSearchStore) SearchChunks(_ context.Context, query string, vec []float32, _ rag.SearchOptions) ([]rag.SearchResult, error) {
	h.calls = append(h.calls, hydeSearchCall2{query: query, vec: vec})
	return h.results, h.err
}

// deterministicEmbedFn returns a non-zero 3-dim vector based on text length.
func deterministicEmbedFn(_ context.Context, text string) ([]float32, error) {
	v := float32(len(text)%5+1) / 5.0
	return []float32{v, 0.5, 0.5}, nil
}

// ---------------------------------------------------------------------------
// T1: HydeConf.Enabled=false → behaves identically to plain SearchChunks
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_Disabled_BaselineEquivalence(t *testing.T) {
	store := &hydeSearchStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "baseline content"}, DocTitle: "Doc1", Score: 1.0},
		},
	}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			t.Error("T1: HypothesisFn must NOT be called when HyDE disabled")
			return "", nil
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled: false,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	results, err := rag.PerformHydeSearch(context.Background(), "test query", deps)
	if err != nil {
		t.Fatalf("T1: unexpected error: %v", err)
	}
	// Disabled path: exactly 1 SearchChunks call with the raw query.
	if got := len(store.calls); got != 1 {
		t.Errorf("T1: want 1 SearchChunks call, got %d", got)
	}
	if len(results) == 0 {
		t.Error("T1: expected results from baseline path")
	}
}

// ---------------------------------------------------------------------------
// T2: HydeConf.Enabled=true → hypothesis + 3-way RRF, hyde-cosine provenance
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_Enabled_HypothesisAndRRF(t *testing.T) {
	store := &hydeSearchStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "semantic match content"}, DocTitle: "SemanticDoc", Score: 0.95},
		},
	}

	hypothesisCalled := 0
	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, query string) (string, error) {
			hypothesisCalled++
			return "a realistic document excerpt that answers: " + query, nil
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	results, err := rag.PerformHydeSearch(context.Background(), "find a semantic document", deps)
	if err != nil {
		t.Fatalf("T2: unexpected error: %v", err)
	}
	if hypothesisCalled != 1 {
		t.Errorf("T2: want 1 hypothesis call, got %d", hypothesisCalled)
	}
	// HyDE path: 3 SearchChunks calls (raw + hyde + pure-vector cosine).
	if got := len(store.calls); got != 3 {
		t.Errorf("T2: want 3 SearchChunks calls, got %d", got)
	}
	if len(results) == 0 {
		t.Error("T2: expected results from HyDE path")
	}
}

// ---------------------------------------------------------------------------
// T3: Hypothesis timeout expired → falls through to BM25, no error
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_HypothesisTimeout_FallsThrough(t *testing.T) {
	store := &hydeSearchStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "bm25 fallback"}, DocTitle: "BM25Doc", Score: 1.0},
		},
	}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(ctx context.Context, _ string) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
				return "late hypothesis", nil
			}
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 5 * time.Millisecond, // force timeout
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	start := time.Now()
	results, err := rag.PerformHydeSearch(context.Background(), "timeout query", deps)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("T3: expected no error on timeout fallthrough, got: %v", err)
	}
	// Fallthrough: exactly 1 SearchChunks call.
	if got := len(store.calls); got != 1 {
		t.Errorf("T3: want 1 SearchChunks call on fallthrough, got %d", got)
	}
	// Should not have waited for the slow hypothesis.
	if elapsed > 150*time.Millisecond {
		t.Errorf("T3: elapsed %v exceeds 150ms — hypothesis timeout not enforced", elapsed)
	}
	// Results from baseline path should be returned.
	if len(results) == 0 {
		t.Error("T3: expected baseline results after timeout fallthrough")
	}
}

// ---------------------------------------------------------------------------
// T4: Hypothesis returns empty string → treat as disabled, fall through
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_HypothesisEmpty_FallsThrough(t *testing.T) {
	store := &hydeSearchStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "result"}, DocTitle: "Doc", Score: 0.8},
		},
	}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "", nil // empty hypothesis
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	results, err := rag.PerformHydeSearch(context.Background(), "empty hypothesis query", deps)
	if err != nil {
		t.Fatalf("T4: unexpected error: %v", err)
	}
	if got := len(store.calls); got != 1 {
		t.Errorf("T4: want 1 SearchChunks call on fallthrough, got %d", got)
	}
	if len(results) == 0 {
		t.Error("T4: expected baseline results")
	}
}

// ---------------------------------------------------------------------------
// T5: Recorder nil-safe — no panic, no record
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_NilRecorder_NoPanic(t *testing.T) {
	store := &hydeSearchStore{}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis text", nil
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
		Recorder:      nil, // explicitly nil — must not panic
	}

	// Should not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("T5: panic with nil Recorder: %v", r)
		}
	}()

	_, err := rag.PerformHydeSearch(context.Background(), "nil recorder test", deps)
	if err != nil {
		t.Fatalf("T5: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// T5b: Recorder non-nil — record is called
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_NonNilRecorder_Records(t *testing.T) {
	store := &hydeSearchStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "content"}, DocTitle: "Doc", Score: 1.0},
		},
	}

	rec := metrics.NewRingRecorder(10)

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis text", nil
		},
		EmbedFn: deterministicEmbedFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
		Recorder:      rec,
	}

	_, err := rag.PerformHydeSearch(context.Background(), "recorder test", deps)
	if err != nil {
		t.Fatalf("T5b: unexpected error: %v", err)
	}

	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Error("T5b: expected at least one metrics event recorded")
	}
	if !snap[0].HydeEnabled {
		t.Error("T5b: expected HydeEnabled=true in recorded event")
	}
}

// Ensure errors package is used.
var _ = errors.New
