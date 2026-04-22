package rag_test

import (
	"testing"

	"daimon/internal/rag"
)

// T1.3: defaults applied correctly, YAML round-trip handled by config package test.

func TestRAGConfig_Defaults(t *testing.T) {
	cfg := rag.RAGConfig{}
	rag.ApplyRAGDefaults(&cfg)

	if cfg.ChunkSize != 512 {
		t.Errorf("expected ChunkSize=512, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 64 {
		t.Errorf("expected ChunkOverlap=64, got %d", cfg.ChunkOverlap)
	}
	if cfg.TopK != 5 {
		t.Errorf("expected TopK=5, got %d", cfg.TopK)
	}
	if cfg.MaxDocuments != 500 {
		t.Errorf("expected MaxDocuments=500, got %d", cfg.MaxDocuments)
	}
	if cfg.MaxChunks != 100000 {
		t.Errorf("expected MaxChunks=100000, got %d", cfg.MaxChunks)
	}
	if cfg.MaxContextTokens != 10000 {
		t.Errorf("expected MaxContextTokens=10000, got %d", cfg.MaxContextTokens)
	}
}

// T14: ApplyRAGDefaults leaves NeighborRadius and thresholds at zero.
func TestApplyRAGDefaults_RetrievalDefaults(t *testing.T) {
	cfg := rag.RAGConfig{}
	rag.ApplyRAGDefaults(&cfg)

	if cfg.Retrieval.NeighborRadius != 0 {
		t.Errorf("NeighborRadius: expected 0 (opt-in), got %d", cfg.Retrieval.NeighborRadius)
	}
	if cfg.Retrieval.MaxBM25Score != 0 {
		t.Errorf("MaxBM25Score: expected 0 (disabled), got %f", cfg.Retrieval.MaxBM25Score)
	}
	if cfg.Retrieval.MinCosineScore != 0 {
		t.Errorf("MinCosineScore: expected 0 (disabled), got %f", cfg.Retrieval.MinCosineScore)
	}
}

func TestRAGConfig_ExplicitValuesPreserved(t *testing.T) {
	cfg := rag.RAGConfig{
		ChunkSize:    256,
		ChunkOverlap: 32,
		TopK:         10,
	}
	rag.ApplyRAGDefaults(&cfg)

	if cfg.ChunkSize != 256 {
		t.Errorf("expected ChunkSize preserved as 256, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 32 {
		t.Errorf("expected ChunkOverlap preserved as 32, got %d", cfg.ChunkOverlap)
	}
	if cfg.TopK != 10 {
		t.Errorf("expected TopK preserved as 10, got %d", cfg.TopK)
	}
}

// T12: ApplyRAGDefaults fills HyDE non-bool defaults when zero-valued.
func TestApplyRAGDefaults_HyDEDefaults(t *testing.T) {
	cfg := rag.RAGConfig{}
	rag.ApplyRAGDefaults(&cfg)

	want := 10 * 1e9 // 10s in nanoseconds
	if float64(cfg.Hyde.HypothesisTimeout) != want {
		t.Errorf("HypothesisTimeout: want 10s, got %v", cfg.Hyde.HypothesisTimeout)
	}
	if cfg.Hyde.QueryWeight != 0.3 {
		t.Errorf("QueryWeight: want 0.3, got %v", cfg.Hyde.QueryWeight)
	}
	if cfg.Hyde.MaxCandidates != 20 {
		t.Errorf("MaxCandidates: want 20, got %d", cfg.Hyde.MaxCandidates)
	}
}

// T13: ApplyRAGDefaults does NOT enable HyDE (opt-in only).
func TestApplyRAGDefaults_HyDE_DisabledByDefault(t *testing.T) {
	cfg := rag.RAGConfig{}
	rag.ApplyRAGDefaults(&cfg)

	if cfg.Hyde.Enabled {
		t.Error("HyDE must default to disabled (opt-in); Enabled was true after ApplyRAGDefaults")
	}
}
