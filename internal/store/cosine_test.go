package store

import (
	"math"
	"testing"
)

func TestCosineSimilarity_IdenticalVectors(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	got := cosineSimilarity(a, a)
	if math.Abs(float64(got)-1.0) > 1e-6 {
		t.Errorf("identical vectors: got %v, want 1.0", got)
	}
}

func TestCosineSimilarity_OrthogonalVectors(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	got := cosineSimilarity(a, b)
	if math.Abs(float64(got)) > 1e-6 {
		t.Errorf("orthogonal vectors: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_ZeroVectorA(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := cosineSimilarity(a, b)
	if got != 0.0 {
		t.Errorf("zero vector a: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_ZeroVectorB(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{0, 0, 0}
	got := cosineSimilarity(a, b)
	if got != 0.0 {
		t.Errorf("zero vector b: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_BothZeroVectors(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{0, 0, 0}
	got := cosineSimilarity(a, b)
	if got != 0.0 {
		t.Errorf("both zero vectors: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	got := cosineSimilarity(a, b)
	if got != 0.0 {
		t.Errorf("length mismatch: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_EmptyVectors(t *testing.T) {
	got := cosineSimilarity([]float32{}, []float32{})
	if got != 0.0 {
		t.Errorf("empty vectors: got %v, want 0.0", got)
	}
}

func TestCosineSimilarity_OppositeVectors(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{-1, 0, 0}
	got := cosineSimilarity(a, b)
	if math.Abs(float64(got)+1.0) > 1e-6 {
		t.Errorf("opposite vectors: got %v, want -1.0", got)
	}
}

func TestCosineSimilarity_Typical256Dim(t *testing.T) {
	// Construct two 256-dim vectors with known structure.
	a := make([]float32, 256)
	b := make([]float32, 256)
	for i := range a {
		a[i] = float32(i) * 0.01
		b[i] = float32(256-i) * 0.01
	}
	got := cosineSimilarity(a, b)
	// Vectors are not orthogonal and not identical — result must be in (-1, 1).
	if got <= -1.0 || got >= 1.0 {
		t.Errorf("typical 256-dim: got %v, expected in (-1, 1)", got)
	}
}

func TestCosineSimilarity_Typical256Dim_SameDirection(t *testing.T) {
	// Two 256-dim vectors pointing in the same direction (one is a scaled version).
	a := make([]float32, 256)
	b := make([]float32, 256)
	for i := range a {
		a[i] = float32(i+1) * 0.01
		b[i] = float32(i+1) * 0.02 // same direction, different magnitude
	}
	got := cosineSimilarity(a, b)
	if math.Abs(float64(got)-1.0) > 1e-4 {
		t.Errorf("scaled same direction: got %v, want ~1.0", got)
	}
}
