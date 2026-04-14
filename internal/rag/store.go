package rag

import (
	"context"
	"errors"
)

// Sentinel errors for the DocumentStore.
var (
	ErrDocNotFound         = errors.New("rag: document not found")
	ErrUnsupportedMIME     = errors.New("rag: unsupported MIME type")
	ErrStorageLimitReached = errors.New("rag: storage limit reached")
)

// DocumentStore persists documents and their vector chunks, and provides
// hybrid (FTS5 + cosine) search.
type DocumentStore interface {
	// AddDocument inserts or replaces a Document record.
	AddDocument(ctx context.Context, doc Document) error

	// AddChunks inserts a batch of chunks for the given document and updates
	// the document's chunk_count.
	AddChunks(ctx context.Context, docID string, chunks []DocumentChunk) error

	// SearchChunks performs FTS5 full-text search on the query string, then
	// optionally reranks with cosine similarity against queryVec (may be nil).
	// Returns up to limit results sorted by relevance descending.
	SearchChunks(ctx context.Context, query string, queryVec []float32, limit int) ([]SearchResult, error)

	// DeleteDocument removes a document and all its chunks (cascade).
	DeleteDocument(ctx context.Context, docID string) error

	// ListDocuments returns all documents in namespace. An empty namespace
	// string returns all documents across all namespaces.
	ListDocuments(ctx context.Context, namespace string) ([]Document, error)
}
