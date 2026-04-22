package rag_test

// Tests for PerformHydeSearch with SkipFTS pure-vector list.
//
//   T8  – Enabled HyDE makes exactly 3 SearchChunks calls; third has SkipFTS=true + empty query + non-empty vec
//   T9  – When raw-BM25 and hyde-BM25 return 0 results but pure-vector returns 3, output contains those 3
//   T10 – event.CosineHits reflects pure-vector list length, not hyde-BM25 length

import (
	"context"
	"testing"
	"time"

	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
)

// skipFTSStore records each SearchChunks call including options.
type skipFTSStore struct {
	calls        []skipFTSCall
	callResults  [][]rag.SearchResult // per-call results: index matches call index
	defaultResult []rag.SearchResult
}

type skipFTSCall struct {
	query string
	vec   []float32
	opts  rag.SearchOptions
}

func (h *skipFTSStore) AddDocument(_ context.Context, _ rag.Document) error { return nil }
func (h *skipFTSStore) AddChunks(_ context.Context, _ string, _ []rag.DocumentChunk) error {
	return nil
}
func (h *skipFTSStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (h *skipFTSStore) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}
func (h *skipFTSStore) GetDocument(_ context.Context, _ string) (rag.Document, error) {
	return rag.Document{}, rag.ErrDocNotFound
}
func (h *skipFTSStore) SearchChunks(_ context.Context, query string, vec []float32, opts rag.SearchOptions) ([]rag.SearchResult, error) {
	idx := len(h.calls)
	h.calls = append(h.calls, skipFTSCall{query: query, vec: vec, opts: opts})
	if idx < len(h.callResults) {
		return h.callResults[idx], nil
	}
	return h.defaultResult, nil
}

// ---------------------------------------------------------------------------
// T8: HyDE enabled → 3 SearchChunks calls; third has SkipFTS=true, empty query, non-empty vec
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_SkipFTS_ThreeCalls(t *testing.T) {
	store := &skipFTSStore{
		defaultResult: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "c1", Content: "semantic"}, Score: 0.9},
		},
	}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis text that is meaningful", nil
		},
		EmbedFn: deterministicEmbedFn, // defined in hyde_search_test.go
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	_, err := rag.PerformHydeSearch(context.Background(), "find semantic docs", deps)
	if err != nil {
		t.Fatalf("T8: unexpected error: %v", err)
	}

	if got := len(store.calls); got != 3 {
		t.Fatalf("T8: want 3 SearchChunks calls, got %d", got)
	}

	// Third call must have SkipFTS=true, empty query, non-empty vector.
	third := store.calls[2]
	if !third.opts.SkipFTS {
		t.Errorf("T8: third call must have SkipFTS=true, got false")
	}
	if third.query != "" {
		t.Errorf("T8: third call must have empty query, got %q", third.query)
	}
	if len(third.vec) == 0 {
		t.Errorf("T8: third call must have non-empty vector")
	}

	// First two calls must NOT have SkipFTS.
	if store.calls[0].opts.SkipFTS {
		t.Error("T8: first call (raw) must NOT have SkipFTS=true")
	}
	if store.calls[1].opts.SkipFTS {
		t.Error("T8: second call (hyde) must NOT have SkipFTS=true")
	}
}

// ---------------------------------------------------------------------------
// T9: When raw-BM25 and hyde-BM25 return 0 results but pure-vector returns 3,
//     final output contains those 3 chunks.
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_SkipFTS_PureVectorOnly_ReturnsResults(t *testing.T) {
	pureVecResults := []rag.SearchResult{
		{Chunk: rag.DocumentChunk{ID: "pv1", Content: "pure vector doc 1"}, Score: 0.95},
		{Chunk: rag.DocumentChunk{ID: "pv2", Content: "pure vector doc 2"}, Score: 0.80},
		{Chunk: rag.DocumentChunk{ID: "pv3", Content: "pure vector doc 3"}, Score: 0.70},
	}

	store := &skipFTSStore{
		callResults: [][]rag.SearchResult{
			nil,            // call 0 (raw): no FTS5 hits
			nil,            // call 1 (hyde): no FTS5 hits
			pureVecResults, // call 2 (pure-vector): 3 hits
		},
	}

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis text for test nine", nil
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

	results, err := rag.PerformHydeSearch(context.Background(), "CV curriculum vitae", deps)
	if err != nil {
		t.Fatalf("T9: unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("T9: expected pure-vector results to propagate into final output, got 0")
	}

	// All returned IDs should come from pureVecResults.
	found := make(map[string]bool)
	for _, r := range results {
		found[r.Chunk.ID] = true
	}
	for _, want := range []string{"pv1", "pv2", "pv3"} {
		if !found[want] {
			t.Errorf("T9: want chunk %s in results, not found (found=%v)", want, found)
		}
	}
}

// ---------------------------------------------------------------------------
// T10: event.CosineHits reflects pure-vector list length, not hyde-BM25 length
// ---------------------------------------------------------------------------

func TestPerformHydeSearch_SkipFTS_CosineHitsReflectsPureVector(t *testing.T) {
	hydeResults := []rag.SearchResult{
		{Chunk: rag.DocumentChunk{ID: "h1", Content: "hyde bm25"}, Score: 0.5},
	}
	pureVecResults := []rag.SearchResult{
		{Chunk: rag.DocumentChunk{ID: "pv1", Content: "pure vec 1"}, Score: 0.95},
		{Chunk: rag.DocumentChunk{ID: "pv2", Content: "pure vec 2"}, Score: 0.85},
	}

	store := &skipFTSStore{
		callResults: [][]rag.SearchResult{
			nil,            // raw: 0
			hydeResults,   // hyde: 1 result
			pureVecResults, // pure-vector: 2 results
		},
	}

	rec := metrics.NewRingRecorder(10)

	deps := rag.HydeSearchDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis for t10 test scenario", nil
		},
		EmbedFn:       deterministicEmbedFn,
		Recorder:      rec,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           true,
			HypothesisTimeout: 2 * time.Second,
			QueryWeight:       0.3,
			MaxCandidates:     10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	_, err := rag.PerformHydeSearch(context.Background(), "CosineHits test", deps)
	if err != nil {
		t.Fatalf("T10: unexpected error: %v", err)
	}

	events := rec.Snapshot()
	if len(events) == 0 {
		t.Fatal("T10: expected at least one recorded event")
	}
	ev := events[len(events)-1]
	// CosineHits must equal len(pureVecResults)=2, NOT len(hydeResults)=1.
	if ev.CosineHits != len(pureVecResults) {
		t.Errorf("T10: want CosineHits=%d (pure-vector count), got %d", len(pureVecResults), ev.CosineHits)
	}
}
