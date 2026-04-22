package rag_test

import (
	"math"
	"testing"

	"daimon/internal/rag"
)

// --- T1: RRFMerge with disjoint lists ---

func TestRRFMerge_DisjointLists(t *testing.T) {
	lists := [][]string{{"a", "b"}, {"c", "d"}}
	scores := rag.RRFMerge(lists, 60)

	// All four chunks must be present.
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, ok := scores[id]; !ok {
			t.Errorf("chunk %q missing from RRF result", id)
		}
	}

	// a and b are both rank 1 and 2 from list 0; c and d rank 1 and 2 from list 1.
	// a=1/61, b=1/62, c=1/61, d=1/62 — a and c tie, b and d tie.
	// At minimum: a score == c score, b score == d score, a > b.
	if math.Abs(scores["a"]-scores["c"]) > 1e-9 {
		t.Errorf("a and c should tie: a=%v c=%v", scores["a"], scores["c"])
	}
	if math.Abs(scores["b"]-scores["d"]) > 1e-9 {
		t.Errorf("b and d should tie: b=%v d=%v", scores["b"], scores["d"])
	}
	if scores["a"] <= scores["b"] {
		t.Errorf("a (rank 1) should score higher than b (rank 2): a=%v b=%v", scores["a"], scores["b"])
	}
}

// --- T2: RRFMerge with overlap ---

func TestRRFMerge_Overlap(t *testing.T) {
	lists := [][]string{{"a", "b", "c"}, {"b", "d"}}
	scores := rag.RRFMerge(lists, 60)

	// b appears in both lists: score = 1/62 + 1/61.
	expectedB := 1.0/62.0 + 1.0/61.0
	if math.Abs(scores["b"]-expectedB) > 1e-9 {
		t.Errorf("b score: want %v, got %v", expectedB, scores["b"])
	}

	// b must beat a, c, d (all single-list).
	for _, id := range []string{"a", "c", "d"} {
		if scores["b"] <= scores[id] {
			t.Errorf("b should outscore %s: b=%v %s=%v", id, scores["b"], id, scores[id])
		}
	}

	// a must beat c (rank 1 vs rank 3 from list 0).
	if scores["a"] <= scores["c"] {
		t.Errorf("a (rank 1) should beat c (rank 3): a=%v c=%v", scores["a"], scores["c"])
	}
}

// --- T3: RRFMerge with three lists, shared chunk x ---

func TestRRFMerge_ThreeLists(t *testing.T) {
	// x appears at rank 2 in list 0, rank 1 in list 1, rank 3 in list 2.
	lists := [][]string{
		{"y", "x"},       // x at rank 1 (0-indexed)
		{"x", "z"},       // x at rank 0
		{"p", "q", "x"},  // x at rank 2
	}
	scores := rag.RRFMerge(lists, 60)

	// x score = 1/(60+2) + 1/(60+1) + 1/(60+3)
	expected := 1.0/62.0 + 1.0/61.0 + 1.0/63.0
	if math.Abs(scores["x"]-expected) > 1e-9 {
		t.Errorf("x score: want %v, got %v", expected, scores["x"])
	}
}

// --- T4: RRFMerge empty input ---

func TestRRFMerge_EmptyInput(t *testing.T) {
	scores := rag.RRFMerge(nil, 60)
	if scores == nil {
		t.Error("expected non-nil map on empty input")
	}
	if len(scores) != 0 {
		t.Errorf("expected empty map, got %v", scores)
	}
}

// --- T5: RRFMerge one empty list ---

func TestRRFMerge_OneEmptyList(t *testing.T) {
	lists := [][]string{{"a"}, {}}
	scores := rag.RRFMerge(lists, 60)

	if _, ok := scores["a"]; !ok {
		t.Error("chunk a must be present")
	}
	if len(scores) != 1 {
		t.Errorf("expected exactly 1 chunk, got %d", len(scores))
	}
}

// --- T6: EnsembleEmbed happy path ---

func TestEnsembleEmbed_HappyPath(t *testing.T) {
	hyp := []float32{1, 0, 0}
	raw := []float32{0, 1, 0}
	result, err := rag.EnsembleEmbed(hyp, raw, 0.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected: normalize(0.7*[1,0,0] + 0.3*[0,1,0]) = normalize([0.7, 0.3, 0])
	// magnitude = sqrt(0.49 + 0.09) = sqrt(0.58)
	mag := math.Sqrt(0.58)
	wantX := float32(0.7 / mag)
	wantY := float32(0.3 / mag)

	if math.Abs(float64(result[0]-wantX)) > 1e-5 {
		t.Errorf("result[0]: want ~%v, got %v", wantX, result[0])
	}
	if math.Abs(float64(result[1]-wantY)) > 1e-5 {
		t.Errorf("result[1]: want ~%v, got %v", wantY, result[1])
	}
	if math.Abs(float64(result[2])) > 1e-7 {
		t.Errorf("result[2]: want 0, got %v", result[2])
	}

	// Result must be unit length.
	var mag2 float64
	for _, v := range result {
		mag2 += float64(v) * float64(v)
	}
	if math.Abs(mag2-1.0) > 1e-5 {
		t.Errorf("result magnitude: want 1.0, got %v", mag2)
	}
}

// --- T7: EnsembleEmbed dimension mismatch ---

func TestEnsembleEmbed_DimensionMismatch(t *testing.T) {
	hyp := []float32{1, 0, 0}
	raw := []float32{0, 1, 0, 0}
	_, err := rag.EnsembleEmbed(hyp, raw, 0.3)
	if err == nil {
		t.Error("expected error on dimension mismatch, got nil")
	}
}

// --- T8: EnsembleEmbed both zero ---

func TestEnsembleEmbed_BothZero(t *testing.T) {
	hyp := []float32{0, 0, 0}
	raw := []float32{0, 0, 0}
	result, err := rag.EnsembleEmbed(hyp, raw, 0.3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Result should be zero vector.
	for i, v := range result {
		if v != 0 {
			t.Errorf("result[%d]: want 0, got %v", i, v)
		}
	}
}

// --- T9: EnsembleEmbed queryWeight bounds ---

func TestEnsembleEmbed_WeightBounds(t *testing.T) {
	hyp := []float32{1, 0, 0}
	raw := []float32{0, 1, 0}

	// weight=0.0 → pure hypothesis direction [1,0,0]
	res0, err := rag.EnsembleEmbed(hyp, raw, 0.0)
	if err != nil {
		t.Fatalf("weight=0.0 error: %v", err)
	}
	// normalize([1,0,0]) = [1,0,0]
	if math.Abs(float64(res0[0]-1.0)) > 1e-5 {
		t.Errorf("weight=0: res[0] want 1.0, got %v", res0[0])
	}
	if math.Abs(float64(res0[1])) > 1e-7 {
		t.Errorf("weight=0: res[1] want 0, got %v", res0[1])
	}

	// weight=1.0 → pure raw direction [0,1,0]
	res1, err := rag.EnsembleEmbed(hyp, raw, 1.0)
	if err != nil {
		t.Fatalf("weight=1.0 error: %v", err)
	}
	// normalize([0,1,0]) = [0,1,0]
	if math.Abs(float64(res1[0])) > 1e-7 {
		t.Errorf("weight=1: res[0] want 0, got %v", res1[0])
	}
	if math.Abs(float64(res1[1]-1.0)) > 1e-5 {
		t.Errorf("weight=1: res[1] want 1.0, got %v", res1[1])
	}

	// No NaN in either result.
	for i, v := range res0 {
		if math.IsNaN(float64(v)) {
			t.Errorf("weight=0: NaN at index %d", i)
		}
	}
	for i, v := range res1 {
		if math.IsNaN(float64(v)) {
			t.Errorf("weight=1: NaN at index %d", i)
		}
	}
}

// --- T10: Provenance single-source chunk ---

func TestProvenance_SingleSource(t *testing.T) {
	lists := map[string][]string{
		"raw-bm25":    {"a", "b"},
		"hyde-bm25":   {"c"},
		"hyde-cosine": {"x"},
	}
	final := []string{"x"}
	prov := rag.Provenance(final, lists)

	if prov["hyde-cosine"] != 1 {
		t.Errorf("hyde-cosine: want 1, got %d", prov["hyde-cosine"])
	}
	if prov["raw-bm25"] != 0 {
		t.Errorf("raw-bm25: want 0, got %d", prov["raw-bm25"])
	}
}

// --- T11: Provenance multi-source chunk ---

func TestProvenance_MultiSource(t *testing.T) {
	lists := map[string][]string{
		"raw-bm25":    {"x", "a"},
		"hyde-cosine": {"x", "b"},
	}
	final := []string{"x"}
	prov := rag.Provenance(final, lists)

	// x is in both raw-bm25 and hyde-cosine, so both counts should be 1.
	if prov["raw-bm25"] != 1 {
		t.Errorf("raw-bm25: want 1, got %d", prov["raw-bm25"])
	}
	if prov["hyde-cosine"] != 1 {
		t.Errorf("hyde-cosine: want 1, got %d", prov["hyde-cosine"])
	}
}
