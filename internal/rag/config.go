package rag

// RAGConfig holds configuration for the Retrieval-Augmented Generation subsystem.
type RAGConfig struct {
	Enabled          bool `yaml:"enabled"             json:"enabled"`
	ChunkSize        int  `yaml:"chunk_size"          json:"chunk_size"`          // default 512
	ChunkOverlap     int  `yaml:"chunk_overlap"       json:"chunk_overlap"`       // default 64
	TopK             int  `yaml:"top_k"               json:"top_k"`               // default 5
	MaxDocuments     int  `yaml:"max_documents"       json:"max_documents"`       // default 500
	MaxChunks        int  `yaml:"max_chunks"          json:"max_chunks"`          // default 100000
	MaxContextTokens int  `yaml:"max_context_tokens"  json:"max_context_tokens"`  // default 10000
}

// ApplyRAGDefaults fills in zero-value fields with documented defaults.
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
}
