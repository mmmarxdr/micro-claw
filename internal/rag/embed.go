package rag

import (
	"encoding/binary"
	"math"
)

// NormalizeEmbedding truncates or zero-pads vec to exactly dim dimensions.
// If len(vec) >= dim, the first dim elements are returned.
// If len(vec) < dim, the vector is zero-padded to dim.
func NormalizeEmbedding(vec []float32, dim int) []float32 {
	if len(vec) >= dim {
		return vec[:dim]
	}
	padded := make([]float32, dim)
	copy(padded, vec)
	return padded
}

// SerializeEmbedding converts a float32 slice to a little-endian binary BLOB.
// Each float32 occupies 4 bytes, so a 256-dim vector produces 1024 bytes.
func SerializeEmbedding(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// DeserializeEmbedding converts a little-endian binary BLOB back to a float32 slice.
// len(data) must be a multiple of 4; any trailing bytes are ignored.
func DeserializeEmbedding(data []byte) []float32 {
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0.0 if either vector has zero magnitude, lengths differ, or either is empty.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dot, magA, magB float64
	for i := range a {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		magA += ai * ai
		magB += bi * bi
	}

	if magA == 0 || magB == 0 {
		return 0.0
	}

	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}
