package rag

import "time"

// Document represents a full document stored in the RAG knowledge base.
type Document struct {
	ID           string    // unique document identifier
	Namespace    string    // scoping (e.g., "global", channel-specific)
	Title        string    // human-readable title
	SourceSHA256 string    // optional: reference to MediaStore blob
	MIME         string    // MIME type of the source content
	ChunkCount   int       // number of chunks this document was split into
	CreatedAt    time.Time // when the document was first ingested
	UpdatedAt    time.Time // when the document was last updated
}

// DocumentChunk is a single chunk from a Document, including its embedding vector.
type DocumentChunk struct {
	ID         string    // unique chunk identifier
	DocID      string    // parent document ID
	Index      int       // zero-based position within the document
	Content    string    // text content of the chunk
	Embedding  []float32 // 256-dim embedding vector (nil when not yet computed)
	TokenCount int       // approximate token count of Content
}

// SearchResult pairs a matching DocumentChunk with its parent document title and score.
type SearchResult struct {
	Chunk    DocumentChunk // the matching chunk
	DocTitle string        // title of the parent document
	Score    float64       // relevance score (higher is better)
}

// ExtractedDoc holds the output of an Extractor — plain text and a title.
type ExtractedDoc struct {
	Title string // extracted or inferred title
	Text  string // full extracted text
}

// ChunkOptions controls how a Chunker splits text.
type ChunkOptions struct {
	Size    int // characters per chunk; default 512
	Overlap int // overlap characters between consecutive chunks; default 64
}
