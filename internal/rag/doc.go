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

	// Fields added in schema v12. All are nullable at the DB layer; zero
	// values are valid defaults for pre-v12 rows that have not yet been
	// touched by the injection counter or the summary worker.
	AccessCount    int        // how many times chunks from this doc were pulled into agent context
	LastAccessedAt *time.Time // when context injection last happened (nil = never)
	Summary        string     // 1-shot LLM summary of the document (empty until Phase B runs)
	PageCount      *int       // page count for paginated formats (PDF/DOCX); nil otherwise

	// IngestedAt (schema v13) is the timestamp the ingestion worker stamped
	// after processJob completed. It is independent of CreatedAt/UpdatedAt
	// because both of those get rewritten on every INSERT OR REPLACE; this
	// one is the authoritative "worker has finished" signal. nil means the
	// worker has not yet processed this row — the API treats that as
	// "indexing".
	IngestedAt *time.Time
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
	Chunk       DocumentChunk // the matching chunk
	DocTitle    string        // title of the parent document
	Score       float64       // relevance score (higher is better); BM25 raw score or RRF score when HyDE is on
	CosineScore *float64      // cosine similarity score when available (nil when no embedding path ran)
}

// ExtractedDoc holds the output of an Extractor — plain text, optional title,
// and optional page count. PageCount is non-nil only for paginated formats
// (PDF, DOCX) and propagates to Document.PageCount during ingestion.
type ExtractedDoc struct {
	Title     string // extracted or inferred title
	Text      string // full extracted text
	PageCount *int   // nil for non-paginated formats (plain text, markdown, html)
}

// ChunkOptions controls how a Chunker splits text.
type ChunkOptions struct {
	Size    int // characters per chunk; default 512
	Overlap int // overlap characters between consecutive chunks; default 64
}

// SearchOptions controls the behavior of SearchChunks.
//
// BM25 vs cosine orientation:
//   - MaxBM25Score is a ceiling: FTS5 bm25() returns lower (more-negative) for
//     better matches. A candidate is dropped when bm25() > MaxBM25Score.
//     Zero means "no threshold" (disabled).
//   - MinCosineScore is a floor: cosine similarity is "higher is better".
//     A candidate is dropped when cosine < MinCosineScore.
//     Applied only on the cosine-rerank path (queryVec provided + ≥2 embeddings).
//     Zero means "no threshold" (disabled).
//
// SkipFTS: when true and queryVec is non-empty, the FTS5 candidate-generation
// step is skipped entirely. SearchChunks iterates all chunks with non-null
// embeddings, computes cosine similarity against queryVec, and returns the
// top-Limit results sorted by cosine descending. MaxBM25Score is ignored on
// this path (no BM25 scores exist). MinCosineScore and NeighborRadius apply
// as normal. When SkipFTS is true and queryVec is empty, nil, nil is returned.
type SearchOptions struct {
	Limit          int     // maximum number of primary results; 0 defaults to 10
	NeighborRadius int     // expand each primary hit by N adjacent chunks; 0 = disabled
	MaxBM25Score   float64 // BM25 ceiling filter; 0 = disabled
	MinCosineScore float64 // cosine floor filter; 0 = disabled
	SkipFTS        bool    // when true, bypass FTS5 and do pure-vector cosine search against all embedded chunks
}
