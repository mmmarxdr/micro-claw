package rag_test

import (
	"context"

	"daimon/internal/rag"
)

// mockExtractor implements rag.Extractor for compile-time interface checking.
type mockExtractor struct{}

func (m *mockExtractor) Extract(_ context.Context, _ []byte, _ string) (rag.ExtractedDoc, error) {
	return rag.ExtractedDoc{}, nil
}

func (m *mockExtractor) Supports(_ string) bool { return false }

// mockChunker implements rag.Chunker for compile-time interface checking.
type mockChunker struct{}

func (m *mockChunker) Chunk(_ string, _ rag.ChunkOptions) []rag.DocumentChunk {
	return nil
}

// mockDocumentStore implements rag.DocumentStore for compile-time interface checking.
type mockDocumentStore struct{}

func (m *mockDocumentStore) AddDocument(_ context.Context, _ rag.Document) error { return nil }
func (m *mockDocumentStore) AddChunks(_ context.Context, _ string, _ []rag.DocumentChunk) error {
	return nil
}
func (m *mockDocumentStore) SearchChunks(_ context.Context, _ string, _ []float32, _ int) ([]rag.SearchResult, error) {
	return nil, nil
}
func (m *mockDocumentStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (m *mockDocumentStore) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}
