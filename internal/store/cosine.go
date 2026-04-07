package store

import (
	"encoding/binary"
	"math"
	"sort"
)

// cosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0.0 if either vector has zero magnitude or if the slices differ in length
// or are empty. This avoids division by zero without panicking.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dot, magA, magB float32
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}

	if magA == 0 || magB == 0 {
		return 0.0
	}

	return dot / (float32(math.Sqrt(float64(magA))) * float32(math.Sqrt(float64(magB))))
}

// normalizeEmbeddingBlob truncates or zero-pads a float32 slice to exactly
// embeddingDims (256) dimensions. Used before cosine comparison on the query path.
//
// Note: the agent package has a similar normalizeEmbedding(vec, dims) function for
// the write path. That version accepts a dims parameter for flexibility; this one
// hardcodes 256 because the store always operates at storage dimension.
func normalizeEmbeddingBlob(vec []float32) []float32 {
	const dims = 256
	if len(vec) >= dims {
		return vec[:dims]
	}
	padded := make([]float32, dims)
	copy(padded, vec)
	return padded
}

// deserializeEmbeddingBlob converts a little-endian binary BLOB back to a
// float32 slice. len(data) must be a multiple of 4; trailing bytes are ignored.
func deserializeEmbeddingBlob(data []byte) []float32 {
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// scoredEntry pairs a MemoryEntry with its cosine similarity score.
type scoredEntry struct {
	entry      MemoryEntry
	similarity float32
}

// sortScoredEntries sorts in-place by similarity descending (highest first).
func sortScoredEntries(results []scoredEntry) {
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})
}
