package rag_test

import (
	"testing"

	"microagent/internal/rag"
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
