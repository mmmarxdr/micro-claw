package rag_test

import (
	"math"
	"testing"

	"daimon/internal/rag"
)

// T2.2: round-trip serialize/deserialize, cosine of identical = 1.0, cosine of orthogonal = 0.0.

func TestSerializeDeserialize_RoundTrip(t *testing.T) {
	original := make([]float32, 256)
	for i := range original {
		original[i] = float32(i) * 0.001
	}

	blob := rag.SerializeEmbedding(original)
	recovered := rag.DeserializeEmbedding(blob)

	if len(recovered) != len(original) {
		t.Fatalf("length mismatch: want %d, got %d", len(original), len(recovered))
	}
	for i := range original {
		if original[i] != recovered[i] {
			t.Errorf("mismatch at index %d: want %v, got %v", i, original[i], recovered[i])
		}
	}
}

func TestNormalizeEmbedding_Truncate(t *testing.T) {
	vec := make([]float32, 512)
	for i := range vec {
		vec[i] = 1.0
	}
	normalized := rag.NormalizeEmbedding(vec, 256)
	if len(normalized) != 256 {
		t.Errorf("expected length 256, got %d", len(normalized))
	}
}

func TestNormalizeEmbedding_Pad(t *testing.T) {
	vec := make([]float32, 10)
	for i := range vec {
		vec[i] = 1.0
	}
	normalized := rag.NormalizeEmbedding(vec, 256)
	if len(normalized) != 256 {
		t.Errorf("expected length 256, got %d", len(normalized))
	}
	// Padded elements should be zero.
	for i := 10; i < 256; i++ {
		if normalized[i] != 0 {
			t.Errorf("expected zero padding at index %d, got %v", i, normalized[i])
		}
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	vec := make([]float32, 256)
	for i := range vec {
		vec[i] = float32(i + 1)
	}
	sim := rag.CosineSimilarity(vec, vec)
	if math.Abs(float64(sim)-1.0) > 1e-5 {
		t.Errorf("expected cosine similarity of identical vectors = 1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := make([]float32, 4)
	b := make([]float32, 4)
	a[0] = 1.0
	b[1] = 1.0
	sim := rag.CosineSimilarity(a, b)
	if math.Abs(float64(sim)) > 1e-6 {
		t.Errorf("expected cosine similarity of orthogonal vectors = 0.0, got %f", sim)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	a := []float32{1.0, 2.0}
	b := []float32{1.0}
	sim := rag.CosineSimilarity(a, b)
	if sim != 0.0 {
		t.Errorf("expected 0.0 for mismatched lengths, got %f", sim)
	}
}

func TestCosineSimilarity_EmptyVectors(t *testing.T) {
	sim := rag.CosineSimilarity(nil, nil)
	if sim != 0.0 {
		t.Errorf("expected 0.0 for empty vectors, got %f", sim)
	}
}
