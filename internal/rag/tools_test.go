package rag_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"microagent/internal/rag"
)

// searchableStore extends trackingStore with configurable SearchChunks result.
type searchableStore struct {
	*trackingStore
	searchResults []rag.SearchResult
}

func newSearchableStore() *searchableStore {
	return &searchableStore{trackingStore: newTrackingStore()}
}

func (s *searchableStore) SearchChunks(_ context.Context, _ string, _ []float32, _ int) ([]rag.SearchResult, error) {
	return s.searchResults, nil
}

// T5.1 — BuildRAGTools

func TestBuildRAGTools_ReturnsTwoTools(t *testing.T) {
	store := newSearchableStore()
	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		MediaStore: newMockMediaStore(),
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	deps := rag.RAGToolDeps{
		Worker:  w,
		Store:   store,
		EmbedFn: nil,
	}
	tools := rag.BuildRAGTools(deps)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if !names["index_doc"] {
		t.Error("expected index_doc tool")
	}
	if !names["search_docs"] {
		t.Error("expected search_docs tool")
	}
}

func TestIndexDocTool_InlineText(t *testing.T) {
	store := newSearchableStore()
	media := newMockMediaStore()

	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		MediaStore: media,
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	deps := rag.RAGToolDeps{Worker: w, Store: store}
	tools := rag.BuildRAGTools(deps)

	var indexTool rag.Tool
	for _, t := range tools {
		if t.Name() == "index_doc" {
			indexTool = t
			break
		}
	}
	if indexTool == nil {
		t.Fatal("index_doc tool not found")
	}

	params, _ := json.Marshal(map[string]any{
		"text":      "Document content here.",
		"title":     "My Document",
		"namespace": "global",
	})

	result, err := indexTool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "My Document") {
		t.Errorf("expected title in response, got: %q", result.Content)
	}

	w.Stop()
}

func TestSearchDocsTool_ReturnsResults(t *testing.T) {
	store := newSearchableStore()
	store.searchResults = []rag.SearchResult{
		{
			Chunk:    rag.DocumentChunk{Content: "relevant chunk content"},
			DocTitle: "Test Document",
			Score:    0.95,
		},
	}

	deps := rag.RAGToolDeps{
		Worker: nil,
		Store:  store,
		EmbedFn: func(_ context.Context, text string) ([]float32, error) {
			return []float32{0.1, 0.2}, nil
		},
	}
	tools := rag.BuildRAGTools(deps)

	var searchTool rag.Tool
	for _, t := range tools {
		if t.Name() == "search_docs" {
			searchTool = t
			break
		}
	}
	if searchTool == nil {
		t.Fatal("search_docs tool not found")
	}

	params, _ := json.Marshal(map[string]any{
		"query":     "relevant content",
		"top_k":     5,
		"namespace": "global",
	})

	result, err := searchTool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Test Document") {
		t.Errorf("expected doc title in results, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "relevant chunk content") {
		t.Errorf("expected chunk content in results, got: %q", result.Content)
	}
}

func TestSearchDocsTool_NoResults(t *testing.T) {
	store := newSearchableStore()

	deps := rag.RAGToolDeps{Store: store}
	tools := rag.BuildRAGTools(deps)

	var searchTool rag.Tool
	for _, tool := range tools {
		if tool.Name() == "search_docs" {
			searchTool = tool
			break
		}
	}
	if searchTool == nil {
		t.Fatal("search_docs tool not found")
	}

	params, _ := json.Marshal(map[string]any{
		"query": "nothing here",
	})

	result, err := searchTool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "no document") {
		t.Errorf("expected 'no documents' message, got: %q", result.Content)
	}
}

// Tool is a local type alias to avoid import cycle in test file
type Tool = interface {
	Name() string
	Execute(ctx context.Context, params json.RawMessage) (rag.ToolResult, error)
}
