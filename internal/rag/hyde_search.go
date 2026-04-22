package rag

import (
	"context"
	"log/slog"
	"time"

	"daimon/internal/rag/metrics"
)

// HydeSearchConfig configures the HyDE retrieval pass for PerformHydeSearch.
// This is a pass-through-safe config type for use inside the rag package.
// Callers (agent loop, search_docs tool) both convert from config.RAGHydeConf.
type HydeSearchConfig struct {
	Enabled           bool
	HypothesisTimeout time.Duration
	QueryWeight       float64
	MaxCandidates     int
}

// RetrievalSearchConfig holds limit and score knobs for the SearchChunks call.
// Mirrors the fields in RAGRetrievalConf that PerformHydeSearch needs.
type RetrievalSearchConfig struct {
	Limit          int
	NeighborRadius int
	MaxBM25Score   float64
	MinCosineScore float64
}

// HydeSearchDeps holds everything PerformHydeSearch needs — store, closures, config.
// All fields are value-safe: HypothesisFn and EmbedFn may be nil (nil means disabled).
// Recorder may be nil — it is treated as a NoopRecorder.
type HydeSearchDeps struct {
	Store         DocumentStore
	HypothesisFn  func(ctx context.Context, query string) (string, error)
	EmbedFn       func(ctx context.Context, text string) ([]float32, error)
	HydeConf      HydeSearchConfig
	RetrievalConf RetrievalSearchConfig
	Recorder      metrics.Recorder // nil-safe
}

// PerformHydeSearch runs the 3-way RRF retrieval:
//   raw-BM25 ∪ hyde-BM25 ∪ hyde-cosine.
//
// Falls through to BM25-only when:
//   - HyDE is disabled (HydeConf.Enabled=false)
//   - HypothesisFn is nil
//   - hypothesis call returns error or empty string
//   - ensemble vector collapses to zero magnitude
//
// This is the SINGLE SOURCE OF TRUTH for HyDE retrieval — both the agent loop
// and the search_docs tool call through here.
// Never returns an error — retrieval must never fail.
func PerformHydeSearch(ctx context.Context, query string, deps HydeSearchDeps) ([]SearchResult, error) {
	totalStart := time.Now()

	baseOpts := SearchOptions{
		Limit:          deps.RetrievalConf.Limit,
		NeighborRadius: deps.RetrievalConf.NeighborRadius,
		MaxBM25Score:   deps.RetrievalConf.MaxBM25Score,
		MinCosineScore: deps.RetrievalConf.MinCosineScore,
	}
	if baseOpts.Limit <= 0 {
		baseOpts.Limit = 5
	}

	event := metrics.Event{
		Timestamp: time.Now(),
		Query:     query,
	}
	recordMetrics := func() {
		if deps.Recorder != nil {
			event.TotalDurationMs = time.Since(totalStart).Milliseconds()
			deps.Recorder.Record(event)
		}
	}

	hydeEnabled := deps.HydeConf.Enabled && deps.HypothesisFn != nil
	if !hydeEnabled {
		results, _ := deps.Store.SearchChunks(ctx, query, embedQuery(ctx, deps.EmbedFn, query), baseOpts)
		event.BM25Hits = len(results)
		event.FinalChunksReturned = len(results)
		event.HydeEnabled = false
		prov := map[string]int{}
		for range results {
			prov["raw-bm25"]++
		}
		event.ProvenanceBreakdown = prov
		recordMetrics()
		return results, nil
	}

	// --- Step 1: Generate hypothesis with timeout ---
	timeout := deps.HydeConf.HypothesisTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	hctx, cancelHyp := context.WithTimeout(ctx, timeout)
	defer cancelHyp()

	hypStart := time.Now()
	hypText, err := deps.HypothesisFn(hctx, query)
	event.HydeDurationMs = time.Since(hypStart).Milliseconds()

	if err != nil {
		slog.Warn("hyde: hypothesis failed, falling through to baseline", "error", err)
		results, _ := deps.Store.SearchChunks(ctx, query, embedQuery(ctx, deps.EmbedFn, query), baseOpts)
		event.BM25Hits = len(results)
		event.FinalChunksReturned = len(results)
		recordMetrics()
		return results, nil
	}
	if hypText == "" {
		slog.Warn("hyde: hypothesis returned empty string, falling through to baseline")
		results, _ := deps.Store.SearchChunks(ctx, query, embedQuery(ctx, deps.EmbedFn, query), baseOpts)
		event.BM25Hits = len(results)
		event.FinalChunksReturned = len(results)
		recordMetrics()
		return results, nil
	}

	// --- Step 2: Embed hypothesis + build ensemble vector ---
	embStart := time.Now()
	queryVec := embedQuery(ctx, deps.EmbedFn, query)
	var hypVec []float32
	if deps.EmbedFn != nil {
		if v, embedErr := deps.EmbedFn(ctx, hypText); embedErr == nil {
			hypVec = v
		}
	}

	queryWeight := deps.HydeConf.QueryWeight
	if queryWeight == 0 {
		queryWeight = 0.3
	}

	ensembleVec, ensembleErr := EnsembleEmbed(hypVec, queryVec, queryWeight)
	event.HydeEmbedMs = time.Since(embStart).Milliseconds()

	if ensembleErr != nil {
		slog.Warn("hyde: ensemble embed failed, falling through to baseline", "error", ensembleErr)
		results, _ := deps.Store.SearchChunks(ctx, query, queryVec, baseOpts)
		event.BM25Hits = len(results)
		event.FinalChunksReturned = len(results)
		recordMetrics()
		return results, nil
	}

	// Zero-vector guard.
	var mag float64
	for _, v := range ensembleVec {
		mag += float64(v) * float64(v)
	}
	if mag < 1e-10 {
		slog.Warn("hyde: ensemble embed collapsed to zero vector, falling through to baseline")
		results, _ := deps.Store.SearchChunks(ctx, query, queryVec, baseOpts)
		event.BM25Hits = len(results)
		event.FinalChunksReturned = len(results)
		recordMetrics()
		return results, nil
	}

	// --- Step 3: Three SearchChunks calls ---
	maxCandidates := deps.HydeConf.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 20
	}
	rawOpts := baseOpts
	rawOpts.Limit = maxCandidates
	hydeOpts := baseOpts
	hydeOpts.Limit = maxCandidates

	// List A: raw query FTS + raw query vector cosine rerank.
	rawResults, _ := deps.Store.SearchChunks(ctx, query, queryVec, rawOpts)
	// List B: hypothesis FTS + ensemble vector cosine rerank.
	hydeResults, _ := deps.Store.SearchChunks(ctx, hypText, ensembleVec, hydeOpts)
	// List C: pure-vector search using ensemble — no FTS5 prefilter.
	// This is the real HyDE contribution: semantic queries surface docs with
	// zero lexical overlap, which is impossible via FTS5 rerank alone.
	cosineOpts := baseOpts
	cosineOpts.Limit = maxCandidates
	cosineOpts.SkipFTS = true
	cosineResults, _ := deps.Store.SearchChunks(ctx, "", ensembleVec, cosineOpts)

	event.BM25Hits = len(rawResults)
	event.CosineHits = len(cosineResults) // reflects actual pure-vector hits, not hyde-BM25
	event.HydeEnabled = true

	// --- Step 4: RRF merge (k=60) across 3 actual lists ---
	rawIDs := make([]string, len(rawResults))
	for i, r := range rawResults {
		rawIDs[i] = r.Chunk.ID
	}
	hydeIDs := make([]string, len(hydeResults))
	for i, r := range hydeResults {
		hydeIDs[i] = r.Chunk.ID
	}
	cosineIDs := make([]string, len(cosineResults))
	for i, r := range cosineResults {
		cosineIDs[i] = r.Chunk.ID
	}

	const rrfK = 60
	lists := [][]string{rawIDs, hydeIDs, cosineIDs}
	scores := RRFMerge(lists, rrfK)

	// Build result lookup — cosineResults included so provenance can find them.
	resultByID := make(map[string]SearchResult, len(rawResults)+len(hydeResults)+len(cosineResults))
	for _, r := range rawResults {
		resultByID[r.Chunk.ID] = r
	}
	for _, r := range hydeResults {
		if _, exists := resultByID[r.Chunk.ID]; !exists {
			resultByID[r.Chunk.ID] = r
		}
	}
	for _, r := range cosineResults {
		if _, exists := resultByID[r.Chunk.ID]; !exists {
			resultByID[r.Chunk.ID] = r
		}
	}

	// Sort by RRF score descending.
	type scored struct {
		id    string
		score float64
	}
	merged := make([]scored, 0, len(scores))
	for id, s := range scores {
		merged = append(merged, scored{id: id, score: s})
	}
	for i := 0; i < len(merged); i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j].score > merged[i].score {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}

	// --- Step 5: Slice to limit ---
	limit := baseOpts.Limit
	if len(merged) > limit {
		merged = merged[:limit]
	}

	final := make([]SearchResult, 0, len(merged))
	finalIDs := make([]string, 0, len(merged))
	for _, sc := range merged {
		if r, ok := resultByID[sc.id]; ok {
			r.Score = sc.score
			final = append(final, r)
			finalIDs = append(finalIDs, sc.id)
		}
	}

	// Provenance tracking.
	provLists := map[string][]string{
		"raw-bm25":    rawIDs,
		"hyde-bm25":   hydeIDs,
		"hyde-cosine": cosineIDs,
	}
	prov := Provenance(finalIDs, provLists)

	event.FinalChunksReturned = len(final)
	event.ProvenanceBreakdown = prov

	slog.Debug("hyde: retrieval complete",
		"final_chunks", len(final),
		"raw_bm25", prov["raw-bm25"],
		"hyde_bm25", prov["hyde-bm25"],
		"hyde_cosine", prov["hyde-cosine"],
	)

	recordMetrics()
	return final, nil
}

// embedQuery is a nil-safe helper: returns nil when embedFn is nil.
func embedQuery(ctx context.Context, embedFn func(context.Context, string) ([]float32, error), text string) []float32 {
	if embedFn == nil {
		return nil
	}
	vec, err := embedFn(ctx, text)
	if err != nil {
		return nil
	}
	return vec
}
