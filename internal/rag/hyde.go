package rag

import (
	"fmt"
	"math"
)

// RRFMerge applies Reciprocal Rank Fusion across multiple ranked lists of chunk IDs.
// k is the smoothing constant (Cormack et al. 2009 recommend 60).
// Each list is rank-ordered; position within the list determines rank (0-indexed).
// Returns a map of chunkID → RRF score. Higher score = better.
func RRFMerge(lists [][]string, k int) map[string]float64 {
	out := make(map[string]float64)
	for _, list := range lists {
		for rank, chunkID := range list {
			// rank+1 because formula uses 1-indexed ranks.
			out[chunkID] += 1.0 / float64(k+rank+1)
		}
	}
	return out
}

// EnsembleEmbed combines a hypothesis embedding and a raw-query embedding into
// a single unit-length vector:
//
//	result = normalize( (1-queryWeight)*hyp + queryWeight*raw )
//
// queryWeight is the weight applied to the raw query component; the hypothesis
// gets (1 - queryWeight). Returns an error when hyp and raw have different lengths.
// Returns a zero vector (no error) when the combined vector has zero magnitude —
// the caller is responsible for detecting the zero-vector case and falling through
// to the baseline retrieval path.
func EnsembleEmbed(hyp, raw []float32, queryWeight float64) ([]float32, error) {
	if len(hyp) != len(raw) {
		return nil, fmt.Errorf("hyde: EnsembleEmbed: dimension mismatch hyp=%d raw=%d", len(hyp), len(raw))
	}
	if len(hyp) == 0 {
		return []float32{}, nil
	}

	hypWeight := 1.0 - queryWeight
	combined := make([]float32, len(hyp))
	for i := range hyp {
		combined[i] = float32(hypWeight)*hyp[i] + float32(queryWeight)*raw[i]
	}

	// L2-normalize so the result is a unit vector suitable for cosine similarity.
	var mag float64
	for _, v := range combined {
		mag += float64(v) * float64(v)
	}
	if mag == 0 {
		// Zero vector — caller guard should detect this via magnitude check.
		return combined, nil
	}
	mag = math.Sqrt(mag)
	for i, v := range combined {
		combined[i] = float32(float64(v) / mag)
	}
	return combined, nil
}

// Provenance counts, per named list, how many chunks from final appeared in that list.
// lists maps a list name (e.g. "raw-bm25", "hyde-bm25", "hyde-cosine") to the
// ordered chunk IDs returned for that list. The returned map records, for each
// list name, the number of final chunks that were sourced from it.
func Provenance(final []string, lists map[string][]string) map[string]int {
	// Build membership sets for each list.
	membership := make(map[string]map[string]bool, len(lists))
	for name, ids := range lists {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		membership[name] = set
	}

	result := make(map[string]int, len(lists))
	for _, chunkID := range final {
		for name, set := range membership {
			if set[chunkID] {
				result[name]++
			}
		}
	}
	return result
}
