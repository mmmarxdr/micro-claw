package rag_test

import (
	"testing"
	"time"

	"daimon/internal/rag"
)

// T1.1: verify types exist and can be instantiated.

func TestDocument_Instantiate(t *testing.T) {
	doc := rag.Document{
		ID:           "doc-1",
		Namespace:    "global",
		Title:        "Test Doc",
		SourceSHA256: "abc123",
		MIME:         "text/plain",
		ChunkCount:   3,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if doc.ID != "doc-1" {
		t.Errorf("expected doc.ID = 'doc-1', got %q", doc.ID)
	}
	if doc.Namespace != "global" {
		t.Errorf("expected doc.Namespace = 'global', got %q", doc.Namespace)
	}
}

func TestDocumentChunk_Instantiate(t *testing.T) {
	chunk := rag.DocumentChunk{
		ID:         "chunk-1",
		DocID:      "doc-1",
		Index:      0,
		Content:    "hello world",
		Embedding:  make([]float32, 256),
		TokenCount: 2,
	}
	if chunk.DocID != "doc-1" {
		t.Errorf("expected chunk.DocID = 'doc-1', got %q", chunk.DocID)
	}
	if len(chunk.Embedding) != 256 {
		t.Errorf("expected embedding length 256, got %d", len(chunk.Embedding))
	}
}

func TestSearchResult_Instantiate(t *testing.T) {
	sr := rag.SearchResult{
		Chunk:    rag.DocumentChunk{ID: "chunk-1"},
		DocTitle: "Test Doc",
		Score:    0.95,
	}
	if sr.Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", sr.Score)
	}
}

func TestExtractedDoc_Instantiate(t *testing.T) {
	ed := rag.ExtractedDoc{
		Title: "My Doc",
		Text:  "Some extracted text",
	}
	if ed.Title != "My Doc" {
		t.Errorf("expected title 'My Doc', got %q", ed.Title)
	}
}

func TestChunkOptions_Instantiate(t *testing.T) {
	opts := rag.ChunkOptions{
		Size:    512,
		Overlap: 64,
	}
	if opts.Size != 512 {
		t.Errorf("expected Size=512, got %d", opts.Size)
	}
	if opts.Overlap != 64 {
		t.Errorf("expected Overlap=64, got %d", opts.Overlap)
	}
}
