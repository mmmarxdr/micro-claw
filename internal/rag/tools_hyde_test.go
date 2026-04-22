package rag_test

// T6–T8: search_docs tool HyDE wiring tests.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
)

// ---------------------------------------------------------------------------
// hydeSearchableStore — tracks calls AND lets test differentiate query strings.
// ---------------------------------------------------------------------------

type hydeSearchableStore2 struct {
	calls        []string // query strings passed to SearchChunks
	resultsByKey map[string][]rag.SearchResult
	defaultResult []rag.SearchResult
}

func newHydeSearchableStore2() *hydeSearchableStore2 {
	return &hydeSearchableStore2{
		resultsByKey: make(map[string][]rag.SearchResult),
	}
}

func (h *hydeSearchableStore2) AddDocument(_ context.Context, _ rag.Document) error  { return nil }
func (h *hydeSearchableStore2) AddChunks(_ context.Context, _ string, _ []rag.DocumentChunk) error {
	return nil
}
func (h *hydeSearchableStore2) DeleteDocument(_ context.Context, _ string) error { return nil }
func (h *hydeSearchableStore2) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}
func (h *hydeSearchableStore2) GetDocument(_ context.Context, _ string) (rag.Document, error) {
	return rag.Document{}, rag.ErrDocNotFound
}
func (h *hydeSearchableStore2) SearchChunks(_ context.Context, query string, _ []float32, _ rag.SearchOptions) ([]rag.SearchResult, error) {
	h.calls = append(h.calls, query)
	if res, ok := h.resultsByKey[query]; ok {
		return res, nil
	}
	return h.defaultResult, nil
}

// findSearchTool locates the search_docs tool from a built tool list.
func findSearchTool(t *testing.T, tools []rag.Tool) rag.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == "search_docs" {
			return tool
		}
	}
	t.Fatal("search_docs tool not found")
	return nil
}

// executeSearch calls the search_docs tool with the given query and top_k.
func executeSearch(t *testing.T, searchTool rag.Tool, query string, topK int) rag.ToolResult {
	t.Helper()
	params, _ := json.Marshal(map[string]any{
		"query": query,
		"top_k": topK,
	})
	result, err := searchTool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("search_docs.Execute error: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// T6: search_docs with hyde.enabled=true — semantic query returns HyDE results
// ---------------------------------------------------------------------------

func TestSearchDocsTool_Hyde_Enabled_SemanticQuery(t *testing.T) {
	// Fixture: "CV" doc only returned when the hypothesis query is used
	// (simulates: lexical match fails, but hypothesis-driven cosine succeeds).
	cvChunk := rag.SearchResult{
		Chunk:    rag.DocumentChunk{ID: "cv-chunk-1", Content: "Experienced software engineer with Go expertise"},
		DocTitle: "CV - John Smith",
		Score:    0.9,
	}

	store := newHydeSearchableStore2()
	// Raw query "curriculum vitae" → no results (simulating lexical miss).
	store.resultsByKey["curriculum vitae"] = nil
	// Hypothesis text → CV results.
	store.defaultResult = []rag.SearchResult{cvChunk}

	hypothesisCalled := 0
	hypothesisFn := func(_ context.Context, _ string) (string, error) {
		hypothesisCalled++
		return "A document listing work experience, education, and professional skills of a candidate.", nil
	}

	deps := rag.RAGToolDeps{
		Store:        store,
		EmbedFn:      deterministicEmbedFn,
		HypothesisFn: hypothesisFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:       true,
			QueryWeight:   0.3,
			MaxCandidates: 10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	tools := rag.BuildRAGTools(deps)
	searchTool := findSearchTool(t, tools)

	result := executeSearch(t, searchTool, "curriculum vitae", 5)

	if result.IsError {
		t.Fatalf("T6: unexpected error result: %s", result.Content)
	}
	if hypothesisCalled != 1 {
		t.Errorf("T6: want 1 hypothesis call, got %d", hypothesisCalled)
	}
	// HyDE makes 3 SearchChunks calls (raw + hyde + pure-vector cosine).
	if got := len(store.calls); got != 3 {
		t.Errorf("T6: want 3 SearchChunks calls, got %d", got)
	}
	// CV doc appears in result.
	if !strings.Contains(result.Content, "CV") {
		t.Errorf("T6: expected CV doc in result, got: %q", result.Content)
	}
}

// ---------------------------------------------------------------------------
// T7: search_docs with hyde.enabled=false — BM25-only path (1 SearchChunks call)
// ---------------------------------------------------------------------------

func TestSearchDocsTool_Hyde_Disabled_BM25Only(t *testing.T) {
	bm25Chunk := rag.SearchResult{
		Chunk:    rag.DocumentChunk{ID: "bm25-chunk-1", Content: "AWS book about cloud services"},
		DocTitle: "AWS Cookbook",
		Score:    0.7,
	}

	store := newHydeSearchableStore2()
	store.defaultResult = []rag.SearchResult{bm25Chunk}

	hypothesisCalled := 0
	deps := rag.RAGToolDeps{
		Store:   store,
		EmbedFn: deterministicEmbedFn,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			hypothesisCalled++
			return "should not be called", nil
		},
		HydeConf: rag.HydeSearchConfig{
			Enabled: false,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}

	tools := rag.BuildRAGTools(deps)
	searchTool := findSearchTool(t, tools)

	result := executeSearch(t, searchTool, "curriculum vitae", 5)

	if result.IsError {
		t.Fatalf("T7: unexpected error: %s", result.Content)
	}
	if hypothesisCalled != 0 {
		t.Errorf("T7: HypothesisFn must not be called when HyDE disabled, got %d calls", hypothesisCalled)
	}
	if got := len(store.calls); got != 1 {
		t.Errorf("T7: want 1 SearchChunks call, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T8: search_docs records metrics event with provenance breakdown when recorder provided
// ---------------------------------------------------------------------------

func TestSearchDocsTool_Hyde_RecordsMetrics(t *testing.T) {
	store := newHydeSearchableStore2()
	store.defaultResult = []rag.SearchResult{
		{Chunk: rag.DocumentChunk{ID: "c1", Content: "content"}, DocTitle: "Doc", Score: 0.8},
	}

	rec := metrics.NewRingRecorder(10)

	deps := rag.RAGToolDeps{
		Store:   store,
		EmbedFn: deterministicEmbedFn,
		HypothesisFn: func(_ context.Context, _ string) (string, error) {
			return "hypothesis text for the query", nil
		},
		HydeConf: rag.HydeSearchConfig{
			Enabled:       true,
			QueryWeight:   0.3,
			MaxCandidates: 10,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
		Recorder:      rec,
	}

	tools := rag.BuildRAGTools(deps)
	searchTool := findSearchTool(t, tools)

	result := executeSearch(t, searchTool, "test metrics query", 5)

	if result.IsError {
		t.Fatalf("T8: unexpected error: %s", result.Content)
	}

	snap := rec.Snapshot()
	if len(snap) == 0 {
		t.Fatal("T8: expected at least 1 metrics event recorded")
	}
	ev := snap[0]
	if !ev.HydeEnabled {
		t.Error("T8: expected HydeEnabled=true in event")
	}
	if ev.Query == "" {
		t.Error("T8: expected non-empty query in event")
	}
	if ev.ProvenanceBreakdown == nil {
		t.Error("T8: expected provenance breakdown in event")
	}
}
