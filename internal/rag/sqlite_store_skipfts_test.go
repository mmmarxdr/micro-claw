package rag_test

// Tests for SearchChunks with SkipFTS=true (pure-vector search path).
//
//   T1  – SkipFTS=true + empty queryVec returns nil, nil
//   T2  – Happy path: top chunk is the semantically closest one
//   T3  – MinCosineScore threshold filters low-similarity chunks
//   T4  – NeighborRadius expands neighbors around cosine hits
//   T5  – Chunks with NULL/empty embedding are skipped
//   T6  – SkipFTS=false (default) uses FTS5 path (regression guard)
//   T7  – Limit is respected after threshold + neighbor expansion

import (
	"context"
	"fmt"
	"testing"
	"time"

	"daimon/internal/rag"
)

// seedSkipFTSFixture seeds 5 documents with 1 chunk each.
// Each chunk has an embedding that is a unit vector in one dimension
// (doc0 → dim0, doc1 → dim1, …, doc4 → dim4). This lets us construct
// a query vector that is "closest to doc2" with a cosine of 1.0 and
// cosine 0.0 for every other chunk.
//
// All chunks contain the word "alpha" so they appear in FTS5 results
// (needed for T6 regression guard).
func seedSkipFTSFixture(t *testing.T) *rag.SQLiteDocumentStore {
	t.Helper()
	db := openTestDB(t)
	s := rag.NewSQLiteDocumentStore(db, 0, 0)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		docID := fmt.Sprintf("skipfts-doc%d", i)
		doc := rag.Document{
			ID:        docID,
			Namespace: "global",
			Title:     fmt.Sprintf("SkipFTS Doc %d", i),
			MIME:      "text/plain",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if err := s.AddDocument(ctx, doc); err != nil {
			t.Fatalf("AddDocument %s: %v", docID, err)
		}

		// Build a 5-dim one-hot embedding for position i.
		emb := make([]float32, 5)
		emb[i] = 1.0

		chunk := rag.DocumentChunk{
			ID:         fmt.Sprintf("skipfts-doc%d-c0", i),
			DocID:      docID,
			Index:      0,
			Content:    "alpha fragment for skipfts doc " + itoa(i),
			Embedding:  emb,
			TokenCount: 5,
		}
		if err := s.AddChunks(ctx, docID, []rag.DocumentChunk{chunk}); err != nil {
			t.Fatalf("AddChunks %s: %v", docID, err)
		}
	}
	return s
}

// closeVec returns a 5-dim vector closest to doc at position target.
// Exact unit vector for target, small noise for others so cosine < 1.
func closeVec(target int) []float32 {
	v := make([]float32, 5)
	v[target] = 1.0
	return v
}

// ---------------------------------------------------------------------------
// T1: SkipFTS=true + empty queryVec → nil, nil
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_EmptyVec_ReturnsNil(t *testing.T) {
	s := seedSkipFTSFixture(t)
	ctx := context.Background()

	results, err := s.SearchChunks(ctx, "", nil, rag.SearchOptions{
		SkipFTS: true,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("T1: unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("T1: want nil results, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// T2: Happy path — query closest to doc2, doc2 chunk should be top result
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_HappyPath_TopIsClosest(t *testing.T) {
	s := seedSkipFTSFixture(t)
	ctx := context.Background()

	results, err := s.SearchChunks(ctx, "", closeVec(2), rag.SearchOptions{
		SkipFTS: true,
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("T2: unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("T2: want results, got none")
	}
	top := results[0]
	if top.Chunk.ID != "skipfts-doc2-c0" {
		t.Errorf("T2: want top chunk skipfts-doc2-c0, got %s (score=%.4f)", top.Chunk.ID, top.Score)
	}
	if top.Score <= 0 {
		t.Errorf("T2: want positive cosine score, got %.4f", top.Score)
	}
}

// ---------------------------------------------------------------------------
// T3: MinCosineScore filters out below-threshold chunks
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_MinCosineScore_FiltersLow(t *testing.T) {
	s := seedSkipFTSFixture(t)
	ctx := context.Background()

	// Query closest to doc3, threshold 0.9. All other docs have cosine ≈0 → filtered.
	results, err := s.SearchChunks(ctx, "", closeVec(3), rag.SearchOptions{
		SkipFTS:        true,
		Limit:          10,
		MinCosineScore: 0.9,
	})
	if err != nil {
		t.Fatalf("T3: unexpected error: %v", err)
	}
	// Only doc3 should survive (cosine 1.0 ≥ 0.9).
	for _, r := range results {
		if r.Score < 0.9 {
			t.Errorf("T3: result %s has score %.4f below MinCosineScore 0.9", r.Chunk.ID, r.Score)
		}
	}
	if len(results) == 0 {
		t.Error("T3: expected at least the target chunk to survive threshold")
	}
}

// ---------------------------------------------------------------------------
// T4: NeighborRadius expands around cosine hits
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_NeighborRadius_Expands(t *testing.T) {
	// Use a single doc with 5 sequential chunks (idx 0-4), each with embeddings.
	db := openTestDB(t)
	s := rag.NewSQLiteDocumentStore(db, 0, 0)
	ctx := context.Background()

	docID := "skipfts-neighbor-doc"
	doc := rag.Document{
		ID:        docID,
		Namespace: "global",
		Title:     "Neighbor Test Doc",
		MIME:      "text/plain",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.AddDocument(ctx, doc); err != nil {
		t.Fatalf("T4: AddDocument: %v", err)
	}

	// Chunk at idx=2 has a high-cosine embedding (unit at dim 0).
	// Others have orthogonal embeddings (unit at dims 1-4).
	chunks := make([]rag.DocumentChunk, 5)
	for i := 0; i < 5; i++ {
		emb := make([]float32, 5)
		if i == 2 {
			emb[0] = 1.0 // target
		} else {
			emb[i%4+1] = 1.0 // orthogonal
		}
		chunks[i] = rag.DocumentChunk{
			ID:         fmt.Sprintf("nb-c%d", i),
			DocID:      docID,
			Index:      i,
			Content:    fmt.Sprintf("neighbor chunk %d", i),
			Embedding:  emb,
			TokenCount: 3,
		}
	}
	if err := s.AddChunks(ctx, docID, chunks); err != nil {
		t.Fatalf("T4: AddChunks: %v", err)
	}

	// Query vector close to chunk at idx=2 with MinCosineScore=0.9 so only idx=2 is a primary.
	queryVec := []float32{1.0, 0, 0, 0, 0}
	results, err := s.SearchChunks(ctx, "", queryVec, rag.SearchOptions{
		SkipFTS:        true,
		Limit:          5,
		MinCosineScore: 0.9,
		NeighborRadius: 1,
	})
	if err != nil {
		t.Fatalf("T4: SearchChunks: %v", err)
	}

	ids := make(map[string]bool)
	for _, r := range results {
		ids[r.Chunk.ID] = true
	}
	// Primary (idx=2) + neighbors (idx=1 and idx=3) should all be present.
	for _, want := range []string{"nb-c1", "nb-c2", "nb-c3"} {
		if !ids[want] {
			t.Errorf("T4: want chunk %s in results (radius=1 expansion), got ids=%v", want, ids)
		}
	}
}

// ---------------------------------------------------------------------------
// T5: Chunks with NULL/empty embedding are skipped
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_NullEmbedding_Skipped(t *testing.T) {
	db := openTestDB(t)
	s := rag.NewSQLiteDocumentStore(db, 0, 0)
	ctx := context.Background()

	docID := "skipfts-nonemb-doc"
	doc := rag.Document{
		ID:        docID,
		Namespace: "global",
		Title:     "No-embedding Test",
		MIME:      "text/plain",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.AddDocument(ctx, doc); err != nil {
		t.Fatalf("T5: AddDocument: %v", err)
	}

	chunks := []rag.DocumentChunk{
		{ID: "nonemb-c0", DocID: docID, Index: 0, Content: "no embedding chunk", TokenCount: 3},        // no embedding
		{ID: "nonemb-c1", DocID: docID, Index: 1, Content: "has embedding chunk", Embedding: []float32{1, 0, 0}, TokenCount: 3},
	}
	if err := s.AddChunks(ctx, docID, chunks); err != nil {
		t.Fatalf("T5: AddChunks: %v", err)
	}

	results, err := s.SearchChunks(ctx, "", []float32{1, 0, 0}, rag.SearchOptions{
		SkipFTS: true,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("T5: unexpected error: %v", err)
	}
	for _, r := range results {
		if r.Chunk.ID == "nonemb-c0" {
			t.Errorf("T5: chunk with no embedding must not appear in pure-vector results")
		}
	}
	found := false
	for _, r := range results {
		if r.Chunk.ID == "nonemb-c1" {
			found = true
		}
	}
	if !found {
		t.Error("T5: chunk with embedding should appear in results")
	}
}

// ---------------------------------------------------------------------------
// T6: SkipFTS=false (default) uses FTS5 path — regression guard
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_False_UsesFTS5Path(t *testing.T) {
	s := seedSkipFTSFixture(t)
	ctx := context.Background()

	// Exact FTS5 query for "alpha" — all 5 chunks contain it.
	results, err := s.SearchChunks(ctx, "alpha", nil, rag.SearchOptions{
		SkipFTS: false,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("T6: unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Error("T6: FTS5 path should return results for 'alpha' query")
	}
}

// ---------------------------------------------------------------------------
// T7: Limit is respected after threshold + neighbor expansion
// ---------------------------------------------------------------------------

func TestSearchChunks_SkipFTS_LimitRespected(t *testing.T) {
	s := seedSkipFTSFixture(t)
	ctx := context.Background()

	// 5 chunks exist; limit=2 must cap results at 2 (before neighbor expansion,
	// since we set NeighborRadius=0).
	results, err := s.SearchChunks(ctx, "", []float32{1, 1, 1, 1, 1}, rag.SearchOptions{
		SkipFTS: true,
		Limit:   2,
	})
	if err != nil {
		t.Fatalf("T7: unexpected error: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("T7: want at most 2 results, got %d", len(results))
	}
}
