package rag

import (
	"time"
)

// RAGHydeConf configures the HyDE (Hypothetical Document Embeddings) pass.
// All fields default to zero/off; users opt in by setting Enabled=true.
// YAML key: rag.hyde
type RAGHydeConf struct {
	Enabled           bool          `yaml:"enabled"            json:"enabled"`
	Model             string        `yaml:"model,omitempty"    json:"model,omitempty"`
	HypothesisTimeout time.Duration `yaml:"hypothesis_timeout" json:"hypothesis_timeout"` // default 10s
	QueryWeight       float64       `yaml:"query_weight"       json:"query_weight"`       // default 0.3
	MaxCandidates     int           `yaml:"max_candidates"     json:"max_candidates"`     // default 20
}

// RAGRetrievalConf holds retrieval-precision knobs for the RAG subsystem.
// All fields default to zero = disabled; users opt in explicitly.
//
// BM25 vs cosine orientation:
//   - MaxBM25Score is a ceiling (FTS5 bm25() returns lower/more-negative for
//     better matches; reject if bm25() > MaxBM25Score). Zero = no threshold.
//   - MinCosineScore is a floor (cosine similarity is "higher is better";
//     reject if cosine < MinCosineScore). Zero = no threshold.
type RAGRetrievalConf struct {
	NeighborRadius int     `yaml:"neighbor_radius"   json:"neighbor_radius"`   // default 0 (opt-in)
	MaxBM25Score   float64 `yaml:"max_bm25_score"    json:"max_bm25_score"`    // default 0 (disabled)
	MinCosineScore float64 `yaml:"min_cosine_score"  json:"min_cosine_score"`  // default 0 (disabled)
}

// RAGMetricsConf configures the in-memory RAG retrieval metrics ring buffer.
// Mirrors config.RAGMetricsConf — keep both in sync (dual-mirror pattern).
// YAML key: rag.metrics
type RAGMetricsConf struct {
	Enabled    bool `yaml:"enabled"     json:"enabled"`      // default true
	BufferSize int  `yaml:"buffer_size" json:"buffer_size"` // default 200
}

// RAGConfig holds configuration for the Retrieval-Augmented Generation subsystem.
type RAGConfig struct {
	Enabled          bool             `yaml:"enabled"             json:"enabled"`
	ChunkSize        int              `yaml:"chunk_size"          json:"chunk_size"`          // default 512
	ChunkOverlap     int              `yaml:"chunk_overlap"       json:"chunk_overlap"`       // default 64
	TopK             int              `yaml:"top_k"               json:"top_k"`               // default 5
	MaxDocuments     int              `yaml:"max_documents"       json:"max_documents"`       // default 500
	MaxChunks        int              `yaml:"max_chunks"          json:"max_chunks"`          // default 100000
	MaxContextTokens int              `yaml:"max_context_tokens"  json:"max_context_tokens"`  // default 10000
	Retrieval        RAGRetrievalConf `yaml:"retrieval"           json:"retrieval"`
	Hyde             RAGHydeConf      `yaml:"hyde"                json:"hyde"`
	Metrics          RAGMetricsConf   `yaml:"metrics"             json:"metrics"`
}

// ApplyRAGDefaults fills in zero-value fields with documented defaults.
// HyDE fields are left at zero/off (opt-in) except for the non-bool fields
// which get sensible defaults so they are ready when Enabled=true is set.
func ApplyRAGDefaults(c *RAGConfig) {
	if c.ChunkSize == 0 {
		c.ChunkSize = 512
	}
	if c.ChunkOverlap == 0 {
		c.ChunkOverlap = 64
	}
	if c.TopK == 0 {
		c.TopK = 5
	}
	if c.MaxDocuments == 0 {
		c.MaxDocuments = 500
	}
	if c.MaxChunks == 0 {
		c.MaxChunks = 100000
	}
	if c.MaxContextTokens == 0 {
		c.MaxContextTokens = 10000
	}
	// HyDE non-bool defaults — applied so they are ready when Enabled is flipped.
	// Enabled stays false (opt-in).
	if c.Hyde.HypothesisTimeout == 0 {
		c.Hyde.HypothesisTimeout = 10 * time.Second
	}
	if c.Hyde.QueryWeight == 0 {
		c.Hyde.QueryWeight = 0.3
	}
	if c.Hyde.MaxCandidates == 0 {
		c.Hyde.MaxCandidates = 20
	}
	// Metrics defaults — collection is ON by default.
	if !c.Metrics.Enabled {
		c.Metrics.Enabled = true
	}
	if c.Metrics.BufferSize == 0 {
		c.Metrics.BufferSize = 200
	}
}
