# SDD Explore — rag-hyde

**Change**: `rag-hyde`
**Phase**: Explore
**Date**: 2026-04-21
**Status**: Complete

---

## Context

HyDE (Hypothetical Document Embeddings, Gao et al. 2022) addresses the mismatch between short query vectors and long document chunk vectors. Instead of embedding the raw query, it asks an LLM to generate a *hypothetical* passage that looks like a relevant document, embeds that passage, and searches with the resulting vector. The intuition: the hypothesis lives in the same embedding space as real chunks, so cosine similarity is more meaningful than query-vs-chunk comparison.

The failing case that motivates this work: user queries `"CV curriculum vitae resume"` — none of those tokens appear in the CV document body. BM25 returns zero CV chunks. Cosine rerank on the raw query also fails because the raw query embed is semantically adjacent to job ads, not a CV's content. A hypothesis like *"John Doe is a software engineer with 8 years of experience..."* would embed near the actual CV chunks.

**Frozen decisions (not re-examined here)**:
1. Mode B: BM25 top-N ∪ HyDE-cosine top-N → merge → final rerank
2. Hypothesis model: `rag.hyde.model` → `rag.summary_model` → main chat provider
3. Single sample per query
4. Ensemble embed: `0.7 * embed(hypothesis) + 0.3 * embed(raw_query)`, renormalized, configurable weights

---

## Codebase Landmarks

| Location | Role |
|---|---|
| `internal/rag/doc.go` | `SearchOptions`, `SearchResult`, `DocumentChunk` types |
| `internal/rag/sqlite_store.go::SearchChunks` | BM25 FTS5 top-50 → optional cosine rerank; current entry point |
| `internal/rag/embed.go` | `CosineSimilarity`, `NormalizeEmbedding`, `SerializeEmbedding` |
| `internal/rag/config.go` | `RAGRetrievalConf`, `RAGConfig` (rag package local copy) |
| `internal/config/config.go::RAGConfig` | Canonical config shape; `RAGEmbeddingConf`, `RAGRetrievalConf` |
| `internal/web/handler_config.go::patchRAG` | **Allow-list for PUT /api/config** — must add `Hyde *RAGHydeConf` here |
| `internal/agent/agent.go` | `ragRetrievalConf`, `ragEmbedFn`, `ragStore` fields |
| `internal/agent/loop.go:128-151` | RAG call site — where HyDE logic must hook |
| `cmd/daimon/rag_wiring.go` | `wireRAG`, `buildSummaryFn`, `buildEmbedFn` — wiring entry |
| `internal/rag/worker.go` | `DocIngestionWorker` — NOT affected by HyDE |

---

## Implementation Architecture

### Q1: Where does the HyDE pass fire?

**Option A — Inside `SearchChunks` (store-layer)**
The store receives a `HypothesisFn func(ctx, query) (string, error)` via `SearchOptions`. When set + `HydeEnabled=true`, it runs the hypothesis generation, embeds the result, merges with the BM25 list, and returns the union.

Pros: Encapsulated; reusable from tools.go and agent loop with a single signature change.
Cons: The store becomes aware of LLM calls, which blurs the hexagonal boundary (store should be a dumb persistence layer). Testing requires mocking the hypothesis fn at the store level. The store also gains a dependency on async LLM latency.

**Option B — In the agent loop (preferred)**
HyDE fires in `loop.go` between line 128 (RAG begins) and line 153 (build system prompt). The agent has access to `ragEmbedFn`, the provider (for hypothesis generation), and all config. It calls the hypothesis generator, computes the ensemble embed, then passes it as `queryVec` to `SearchChunks` — alongside a separate HyDE-only `SearchChunks` call for the cosine top-N.

Pros:
- Store interface stays unchanged (`SearchChunks` signature is not touched).
- BM25 call and HyDE cosine call are independently testable.
- The merge algorithm lives in the agent layer, where all the config context is available.
- Hypothesis fn is a `func(context.Context, string) (string, error)` — injectable in tests without touching the store interface.

Cons:
- The loop grows slightly (20-30 lines). Acceptable; the loop already handles embedding.
- `WithHydeConf` becomes a new agent setter (mirrors `WithRAGRetrievalConf`).

**Verdict: Option B.** Keep the store interface clean. The agent loop already owns the embedding call; adding the hypothesis step is a natural extension of the same block.

### Q2: Candidate Merge Algorithm

Three options to merge BM25 top-N (list A) and HyDE-cosine top-N (list B):

**(a) Round-robin / dual-list**
Interleave by rank: A[0], B[0], A[1], B[1]... Remove duplicates by chunk ID. Final rerank by cosine against raw query (or ensemble embed).
- Pro: Simple, no score calibration needed.
- Con: Round-robin bias favors whichever list is "first"; doesn't reflect actual relevance differences.

**(b) Min-max normalization, weighted sum**
Normalize BM25 scores from [min,max] → [0,1] (note: BM25 is negative, so invert). Normalize cosine scores (already 0..1). Weighted sum: `α*bm25_norm + (1-α)*cosine`.
- Pro: Principled combination of both signals.
- Con: Min-max is sensitive to outliers. When one list is very small, normalization is noisy. More code, more parameters.

**(c) Reciprocal Rank Fusion (RRF) — recommended**
`RRF(d) = Σ 1 / (k + rank_i(d))` where k=60 (classic default). Each document's score is the sum of reciprocal ranks across all lists it appears in. No score normalization needed — only rank positions matter.
- Pro: Sidesteps BM25-vs-cosine calibration entirely (scores live in incompatible spaces). Robust to missing entries (a chunk absent from one list simply has no contribution from it). Standard technique in information retrieval (Cormack et al. 2009). Minimal code (~10 lines).
- Con: Loses score magnitude. But for final context injection (not ranking across users), rank quality matters more than score precision.

**Verdict: RRF.** It is the simplest to implement correctly with what's in the codebase (only rank positions needed, no float math beyond the formula). `k=60` is the published default; expose as `rag.hyde.rrf_k` for tuning.

### Q3: Concrete Merge Flow (Mode B with RRF)

```
1. Embed raw query → rawVec (already done today)
2. If hyde.enabled:
   a. Call hypothesisFn(ctx, rawQuery) → hypothesisText (with timeout)
   b. Embed hypothesisText → hypothesisVec
   c. ensembleVec = normalize(hyde.query_weight*hypothesisVec + (1-hyde.query_weight)*rawVec)
   d. Call SearchChunks(ctx, rawQuery, nil, {Limit: hyde.max_candidates}) → bm25Results
   e. Call SearchChunks(ctx, "", ensembleVec, {Limit: hyde.max_candidates}) → hydeResults
      (FTS query is empty/stripped → only cosine path fires; need store to support queryVec-only mode)
   f. Build merged set: dedup by chunk ID, assign RRF scores
   g. Sort by RRF score descending, take top ragMaxChunks
3. Else: existing path (SearchChunks with rawQuery + rawVec)
```

**Store note**: Step (e) requires passing a non-empty `queryVec` with an empty or trivially-sanitized FTS query. Looking at `sanitizeFTSQuery`: an empty string returns `""`, and SearchChunks returns `nil, nil` early. This means HyDE cosine-only search cannot currently be done by passing an empty query string — the function short-circuits before reaching the cosine path. **This is a real implementation constraint.** Options:
- Add `SearchOptions.SkipFTS bool` — when true, skip the FTS query and go straight to a cosine-only scan of all stored chunks (expensive: full table scan).
- Keep the two-call approach but pass a synthetic FTS query that always matches (e.g., all tokens from the hypothesis, which has real words) — use the hypothesis text as the FTS query string in the HyDE cosine call. This is actually sensible: hypothesis has content-bearing words, BM25 might surface different chunks than the pure ensemble embed.
- Alternative: merge at the agent layer before calling SearchChunks at all, using a single joint call.

**Recommended approach**: Use the hypothesis text itself as the FTS query for the HyDE SearchChunks call. This avoids needing to change `SearchChunks` at all, and gives a third signal (hypothesis-BM25) for free. The merge is still chunk ID dedup + RRF across all three lists (raw-BM25, hyde-BM25, hyde-cosine). The user gets `max_candidates` from each list, so total candidates before dedup ≤ 3×max_candidates.

### Q4: Failure Modes and Guardrails (policy only)

| Failure | Policy |
|---|---|
| `hypothesisFn` errors (rate limit, timeout, provider down) | Log at `slog.Warn` level. Fall through to BM25-only path. Do NOT fail retrieval. |
| `hypothesisFn` returns empty string | Skip HyDE entirely for this turn. Log at debug. Fall through to BM25-only. |
| Ensemble embed is zero vector (both inputs produced all-zeros) | Skip HyDE cosine search. Fall through to BM25+raw-cosine. Detect via magnitude check before normalize. |
| Query < 3 tokens (too short to generate meaningful hypothesis) | Still attempt; the LLM may still produce useful output. Do not short-circuit on length alone. |
| `hypothesis_timeout` exceeded | Context cancel propagated to provider call; log warn, fall through. |
| Embedding provider returns wrong dimension | `NormalizeEmbedding` already handles truncation/padding; this is safe. |

### Q5: Configuration Surface

**YAML shape** (`rag.hyde.*`):

```yaml
rag:
  hyde:
    enabled: false              # opt-in; default false
    model: ""                   # empty = use rag.summary_model → main provider
    hypothesis_timeout: 10s     # context deadline for the LLM call
    query_weight: 0.7           # weight for hypothesis embed in ensemble
    max_candidates: 20          # top-N per list (BM25 + HyDE cosine each)
    rrf_k: 60                   # RRF constant; 60 is the published default
```

**Go struct** (mirrors pattern in `internal/config/config.go`):

```go
// RAGHydeConf configures the HyDE (Hypothetical Document Embeddings) pass.
// YAML key: rag.hyde
type RAGHydeConf struct {
    Enabled            bool          `yaml:"enabled"             json:"enabled"`
    Model              string        `yaml:"model,omitempty"     json:"model,omitempty"`       // empty = fallback chain
    HypothesisTimeout  time.Duration `yaml:"hypothesis_timeout"  json:"hypothesis_timeout"`   // default 10s
    QueryWeight        float64       `yaml:"query_weight"        json:"query_weight"`          // default 0.7
    MaxCandidates      int           `yaml:"max_candidates"      json:"max_candidates"`        // default 20
    RRFK               int           `yaml:"rrf_k"               json:"rrf_k"`                // default 60
}
```

This struct must be added to:
1. `internal/config/config.go::RAGConfig` as `Hyde RAGHydeConf`
2. `internal/rag/config.go` (local rag-package copy, if it mirrors config) — check: it does mirror `RAGRetrievalConf` at both locations.
3. `internal/web/handler_config.go::patchRAG` as `Hyde *RAGHydeConf` — **critical, or PUT /api/config silently drops it**.
4. `cmd/daimon/rag_wiring.go::wireRAG` to wire `hypothesisFn` and pass `Hyde` config to the agent.
5. `internal/agent/agent.go` — new field `ragHydeConf config.RAGHydeConf` + `WithHydeConf` setter.
6. `internal/config/config.go::ApplyDefaults` — set `HypothesisTimeout=10s`, `QueryWeight=0.7`, `MaxCandidates=20`, `RRFK=60`.

**patchRAG allow-list regression**: Current `patchRAG` has `Embedding` and `Retrieval`. Adding `Hyde *config.RAGHydeConf` here is mandatory. Without it, any PUT that includes `rag.hyde` gets silently dropped by the JSON decoder.

---

## Agent Field and Wiring

The agent needs one new field and one new setter:

```go
// in agent.go Agent struct
ragHydeConf     config.RAGHydeConf
ragHypothesisFn func(context.Context, string) (string, error) // nil = HyDE disabled
```

```go
// WithHydeConf wires the HyDE hypothesis generator and config.
func (a *Agent) WithHydeConf(conf config.RAGHydeConf, fn func(context.Context, string) (string, error)) *Agent {
    a.ragHydeConf = conf
    a.ragHypothesisFn = fn
    return a
}
```

In `cmd/daimon/rag_wiring.go::wireRAG`, build the `hypothesisFn` similarly to `buildSummaryFn`:
- Resolve model: `hyde.Model` → `ragCfg.SummaryModel` → empty (provider default).
- The prompt shape is different: "Write a short passage that would answer this question: {query}" or equivalent. This prompt design is left to sdd-propose.

---

## Testing Strategy

### Unit Tests

HyDE is non-deterministic (LLM-generated hypothesis). Make it deterministic by injecting `HypothesisFn`:

```go
// Inject a deterministic stub:
mockHypothesisFn := func(_ context.Context, query string) (string, error) {
    return "hypothetical passage about " + query, nil
}
```

Test cases to cover:
- HyDE disabled → existing SearchChunks behavior unchanged (regression)
- HyDE enabled, hypothesisFn succeeds → union of BM25 + HyDE candidates, RRF scores assigned, top-K returned
- HyDE enabled, hypothesisFn returns error → falls through to BM25-only (guardrail)
- HyDE enabled, hypothesisFn returns empty string → falls through to BM25-only
- Ensemble embed calculation: given two known vectors, verify weighted sum and renormalization
- RRF scoring: given two ranked lists with overlap, verify scores and final order
- Dedup: same chunk ID in both lists appears exactly once in merged result

Unit test location: `internal/rag/hyde_test.go` (if HyDE logic is extracted to a helper) or `internal/agent/loop_hyde_test.go` (if tested at agent layer). The RRF and ensemble math should be extracted to pure functions in `internal/rag/hyde.go` for independent unit testing.

### Evaluation Suite (precision@5)

**Storage**: `internal/rag/testdata/eval_queries.json`

**Schema sketch**:
```json
[
  {
    "id": "q01",
    "query": "...",
    "category": "semantic|lexical|mixed|edge",
    "ground_truth_chunk_ids": ["chunk-abc", "chunk-def"]
  }
]
```

**10 query set** (based on known indexed docs: CV, AWS book, k8s book):

| ID | Query | Category | Rationale |
|---|---|---|---|
| q01 | "curriculum vitae software engineer experience" | semantic | The exact failing case; no keyword overlap with CV content |
| q02 | "professional background work history" | semantic | Synonyms for CV content |
| q03 | "AWS S3 bucket lifecycle policy" | lexical | Exact AWS terminology appears verbatim in book |
| q04 | "kubectl describe pod error" | lexical | Exact k8s CLI terms |
| q05 | "container orchestration scheduling" | mixed | Some terms in k8s book, some semantic |
| q06 | "distributed storage object versioning" | mixed | Semantic + partial lexical (S3 book) |
| q07 | "hire me" | edge (ultra-short semantic) | 2 tokens; should surface CV |
| q08 | "expérience professionnelle" | edge (different language) | French for "professional experience"; tests cross-lingual semantic |
| q09 | "EKS node group autoscaling" | lexical (specific) | Niche k8s/AWS term |
| q10 | "achievements accomplishments impact" | semantic | Career highlights language vs CV content |

**Scoring script**: `internal/rag/eval/score.go` (or similar). For each query, run `SearchChunks` (baseline) and HyDE search, compute `precision@5 = |relevant ∩ top5| / 5`. Output JSON with before/after per query and overall delta. Must be runnable as `go test -run=TestEvalPrecision -tags=eval ./internal/rag/...` with an integration tag so it doesn't run in CI without a live DB.

Ground truth chunk IDs require a one-time human labeling pass against the user's actual indexed content (inspectable via `GET /api/knowledge`). This is out of scope for the implementation phase — flag for sdd-verify.

---

## Cost and Latency Estimate

**Per-retrieval cost (HyDE enabled)**:

| Step | Tokens (est.) | Cost (Gemini Flash 2.5) |
|---|---|---|
| Hypothesis input | ~200 (query + prompt) | $0.075/1M × 0.0002 = **$0.000015** |
| Hypothesis output | ~200 (hypothesis text) | $0.30/1M × 0.0002 = **$0.000060** |
| Embed (hypothesis) | 1 API call | ~$0 (embedding is separate, negligible) |
| **Total per retrieval** | | **~$0.000075** (~7.5 hundredths of a cent) |

At 50 retrievals/hour: **$0.00375/hour** = **$0.09/day**. Negligible.

**Per-retrieval latency**:
- One LLM round-trip: ~500-1500ms (Gemini Flash 2.5 speed)
- One embed call: ~100-200ms
- Two `SearchChunks` calls instead of one: ~5-20ms each (SQLite local)
- **Total overhead**: ~600-1700ms per turn that uses RAG

This is the main UX tradeoff. The hypothesis call is serial with retrieval. The embed of hypothesis can be parallelized with the BM25 search call, but not with the hypothesis generation itself.

---

## Prior Art Summary (context7 / literature)

Context7 does not have a Go-specific HyDE library. Findings from the original paper and practice:

- **HyDE (Gao et al. 2022)**: Generates a *zero-shot* hypothetical document, embeds it, uses it for retrieval. Works well for asymmetric retrieval (short query vs. long passage). The key insight is that the hypothesis is closer in token distribution to the target passages than the query itself.
- **RRF (Cormack et al. 2009)**: `score(d) = Σ 1/(k+r_i)` where r_i is rank of d in list i, k=60 prevents division by very small numbers. Standard in TREC fusion tasks. Consistently outperforms score-based fusion when score spaces differ.
- **Go RAG patterns**: No mature Go RAG library with HyDE support found on context7. The patterns are paper-level, not library-level. Daimon's implementation will be from scratch, which is fine given the codebase's zero-dependency philosophy.

---

## Open Questions

1. **HyDE cosine-only search in SearchChunks**: Passing an empty FTS query short-circuits to `nil, nil` before the cosine path. The recommended workaround (use hypothesis text as FTS query) gives a useful third signal but changes the semantics of the "HyDE cosine" list slightly (it becomes BM25(hypothesis) + cosine(ensemble)). Is this acceptable, or does the user want a pure cosine-only path? If pure cosine is needed, `SearchOptions.SkipFTS bool` must be added to the store interface.

2. **Hypothesis prompt design**: What exact prompt produces the best hypotheses for Daimon's use case? The explore phase leaves this to sdd-propose. Initial candidates: (a) "Write a short passage that would answer: {query}" (paper original), (b) "Write the kind of text that would be relevant to: {query}" (less prescriptive). The prompt shapes the hypothesis, which shapes the embed.

3. **`ragHypothesisFn` vs. `ragHydeConf.Model` resolution at wiring time**: Should the model fallback chain (`hyde.model` → `summary_model` → provider default) be resolved at wire time (in `rag_wiring.go`) or deferred to call time (in the hypothesis closure)? At wire time is simpler and more debuggable. Flag for proposal phase.

4. **RRF list count**: If the approach uses three lists (raw-BM25, hyde-BM25, hyde-cosine), the RRF formula sums across all three. Is three-way RRF the right default, or should it be two-way (BM25 ∪ hyde-cosine only, ignoring hyde-BM25)? The hypothesis-text BM25 list is a byproduct of not having a SkipFTS path, not a deliberate design choice. This should be decided in proposal.

---

## Key Non-Obvious Findings

1. **The `sanitizeFTSQuery` short-circuit is a blocker for pure HyDE-cosine-only search.** Any implementation that wants to search by vector only (no FTS) must either add `SearchOptions.SkipFTS` or use the hypothesis text as the FTS query.

2. **`patchRAG` allow-list in `handler_config.go` is the most common regression point.** Every new `rag.*` config field must be added there explicitly. The comment already says this. It must be in the task checklist.

3. **`rag.RAGRetrievalConf` and `config.RAGRetrievalConf` are two separate type declarations in two packages** (`internal/rag/config.go` and `internal/config/config.go`). Both must be updated when adding fields. The agent uses `rag.RAGRetrievalConf` (line 108 agent.go), the config file uses `config.RAGRetrievalConf`, and `wireRAG` bridges them. The same dual-location pattern will apply to `RAGHydeConf`.

4. **`buildSummaryFn` in `rag_wiring.go` is the exact template for `buildHypothesisFn`.** It takes `(prov provider.Provider, modelOverride string)`, returns a closure. HyDE hypothesis function will have the same signature except the prompt differs and the output is used as a query, not stored.

5. **The agent's RAG block (`loop.go:128-151`) is already structured as a single isolated block.** HyDE can be inserted cleanly as a conditional branch within that block without restructuring the loop.
