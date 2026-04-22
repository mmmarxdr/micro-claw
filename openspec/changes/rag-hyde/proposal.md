# Proposal: RAG HyDE (Hypothetical Document Embeddings) + Retrieval Metrics + Docs Sweep

## 1. Why

- **The CV failure case is the canonical witness.** User query `"CV curriculum vitae resume"` returns zero chunks: BM25 finds no overlap (the CV body never uses those meta-words) and cosine rerank can't help because FTS5 already short-circuited before any embedding comparison runs (`internal/rag/sqlite_store.go:149-152`). Precision@5 on semantic queries is effectively 0 whenever the vocabulary gap is larger than the lexical overlap.
- **The rerank path is structurally subordinate to BM25.** `SearchChunks` first selects the FTS top-50 and only then considers embeddings — any semantically-relevant chunk that fails to share a keyword with the query never enters the candidate pool in the first place. Cosine cannot recover candidates it never sees. This is a recall problem, not a ranking problem.
- **We ship RAG precision knobs but have no way to tell whether they help.** `rag-retrieval-precision` added neighbor expansion + score thresholds, and `rag.embedding` added a dedicated embedding provider, yet there is no instrumentation on the retrieval path. We can't A/B a change, can't inspect regressions, can't see which list contributed a hit, can't benchmark HyDE vs baseline on real queries. Operators are flying blind.
- **Docs drifted during v0.7.0 / v0.8 ship velocity.** `rag.embedding`, `rag.retrieval`, the knowledge tab, the `daimon web` subcommand, and the upcoming `rag.hyde` block are not documented in `README.md`, `docs/CONFIG.md`, `DAIMON.md`, or `TESTS.md`. New operators can't discover features that already exist.

This change fixes all three in one PR split into three commits, because they share the same test scaffolding (mock embed/chat/hypothesis fns) and the same config-plumbing code paths (`patchRAG`, `ApplyRAGDefaults`, dual-mirror `config.RAG*Conf` ↔ `rag.RAG*Conf`).

## 2. Architectural Decisions (all frozen upstream — restated for implementers)

The sdd-explore phase and the orchestrator-level freeze locked in 20 decisions. Every implementer MUST treat this section as the contract.

### HyDE core

1. **Integration lives in the agent loop, not the store.** New injectable field `Agent.ragHypothesisFn func(ctx, query) (string, error)` and new `WithHydeConf` setter. The hook site is `internal/agent/loop.go:128-151` — the existing RAG block is already a self-contained region, so HyDE inserts as a conditional branch without restructuring. **Why:** keeps `DocumentStore` a dumb persistence layer (hexagonal boundary), keeps LLM-latency concerns out of SQL code, keeps the RRF merge where all config is already available.

2. **Three-way Reciprocal Rank Fusion (RRF), k=60.** Three candidate lists: raw-BM25 (current path), hyde-BM25 (hypothesis text used as FTS query), hyde-cosine (ensemble-embed rerank). RRF formula `score(d) = Σ 1/(k + rank_i(d))`. **Why:** score spaces are incompatible (BM25 negative-is-better, cosine [0,1]-higher-is-better) and min-max normalization is outlier-sensitive; RRF needs only rank positions (Cormack et al. 2009). Three-way is free because `sanitizeFTSQuery` refuses empty strings (`sqlite_store.go:149-152`) — we need a non-empty FTS query for the HyDE call anyway, so reusing the hypothesis text as that query produces a third useful signal at zero extra cost. No `SearchOptions.SkipFTS` flag, no `DocumentStore` interface change.

3. **Provenance is logged per final hit.** Every chunk in the final merged set records which of the three lists contributed it (`raw-bm25` | `hyde-bm25` | `hyde-cosine` | combinations). **Why:** operators must be able to see *why* a chunk surfaced when debugging retrieval regressions; feeds the metrics event schema (§5).

4. **Ensemble embedding = `0.7 * embed(hypothesis) + 0.3 * embed(raw_query)`, re-normalized.** Configurable via `rag.hyde.query_weight` (the weight on the *raw* query side; hypothesis weight is `1 - query_weight`). Default `query_weight = 0.3`. **Why:** pure hypothesis embedding can overshoot and miss literal matches; blending preserves some raw-query anchor. The 0.3/0.7 split is the explore-phase recommendation based on HyDE literature and asymmetric-retrieval practice.

5. **Single-sample HyDE — one hypothesis per query.** No multi-sample averaging. **Why:** latency + cost bound (one extra LLM round-trip per retrieval is already +600–1700 ms per RAG turn per explore §cost-and-latency). Multi-sample stays in "out of scope" pending evaluation data.

6. **Hypothesis model fallback chain resolved at WIRE TIME**, not call time. `rag.hyde.model → rag.summary_model → main chat provider`. Mirrored from `buildSummaryFn` (`cmd/daimon/rag_wiring.go:255-278`). **Why:** wire-time resolution is simpler, more debuggable (`slog.Info` emits the resolved model at startup), and avoids per-call branching. A new helper `buildHypothesisFn(prov, modelOverride)` mirrors `buildSummaryFn` exactly, differing only in prompt and output handling.

7. **Hypothesis prompt (exact text, no variables beyond `{q}`):**
   ```
   Write a realistic excerpt from a document that would best answer the user's query.
   Write 2-4 sentences, plain prose, no preamble or framing, as if extracted verbatim.
   If the query asks to find or summarize a document, describe what that document contains.

   Query: {q}
   ```
   **Why:** "realistic excerpt" pushes the LLM toward passage-shaped text (close to real chunks in embedding space); the "find/summarize" carveout handles meta-queries like "CV resume" where the user is looking FOR the document rather than for content inside it.

8. **Default OFF globally via `ApplyRAGDefaults`.** The user's own `~/.daimon/config.yaml` receives an explicit `rag.hyde.enabled: true` line appended during rollout — this is a documented manual config edit, NOT a default change. **Why:** HyDE adds ~1s latency per RAG turn and ~$0.09/day cost per user — we must not impose this silently. Opt-in is consistent with `rag.retrieval.neighbor_radius`, `rag.retrieval.max_bm25_score`, and `rag.retrieval.min_cosine_score` (all default zero/disabled).

9. **Config struct dual-mirror pattern.** New `RAGHydeConf{Enabled, Model, HypothesisTimeout, QueryWeight, MaxCandidates}` added to BOTH `config.RAGConfig` (`internal/config/config.go:537`) and the rag-package local copy (`internal/rag/config.go:18`). **Why:** this is the established pattern — `RAGEmbeddingConf` and `RAGRetrievalConf` are both declared in both packages (`internal/config/config.go:529` and `internal/rag/config.go:11`). Not mirroring would break the existing wiring bridge in `wireRAG`. **`patchRAG` MUST be extended with `Hyde *config.RAGHydeConf`** (`internal/web/handler_config.go:42-45`) — the allow-list silently drops unknown fields, so without this a PUT from the UI would claim success while the value never reaches disk. This is the canonical regression flagged in rag-retrieval-precision T16 and must be guarded by a mirror test.

10. **Failure policy: never fail retrieval.** Hypothesis fn errors, context timeout, empty output, zero-vector ensemble embed → `slog.Warn`, skip HyDE for this turn, fall through to the existing raw-BM25 + raw-cosine path. The user's question still gets an answer, just without the HyDE lift. **Why:** HyDE is a precision enhancer, not a correctness requirement. A provider rate-limit must not break the chat.

### Metrics (RAG-wide, not HyDE-only)

11. **Scope is RAG-wide.** We instrument both the existing hybrid path AND the new HyDE path, because otherwise we can't A/B. Metrics live behind the same flag as retrieval itself (always on when RAG is on).

12. **Storage: in-memory ring buffer, N=200 events.** No SQLite persistence. Resets on process restart. **Why:** diagnostic timeline, not a BI dashboard. SQLite persistence costs a migration, schema ownership, and retention policy — none justified until we have a UI that actually surfaces this data.

13. **Event schema** (`rag.MetricsEvent`):
    | Field | Type | Meaning |
    |---|---|---|
    | `timestamp` | `time.Time` | RFC3339 at record time |
    | `query` | `string` | Truncated to 80 runes (not bytes — multi-byte safe) |
    | `total_duration_ms` | `int64` | Wall time of the whole retrieval block |
    | `bm25_hits` | `int` | Count of raw-BM25 candidates before filtering |
    | `hyde_enabled` | `bool` | Whether HyDE fired this turn |
    | `hyde_duration_ms` | `int64` | Hypothesis-generation wall time (0 when disabled / failed) |
    | `hyde_embed_ms` | `int64` | Ensemble-embedding wall time (0 when disabled / failed) |
    | `cosine_hits` | `int` | Count after cosine rerank / hyde-cosine list size |
    | `neighbors_expanded` | `int` | Extra chunks added by `NeighborRadius` |
    | `threshold_rejected_bm25` | `int` | Count dropped by `MaxBM25Score` |
    | `threshold_rejected_cosine` | `int` | Count dropped by `MinCosineScore` |
    | `final_chunks_returned` | `int` | What the agent loop actually received |
    | `provenance_breakdown` | `map[string]int` | `raw-bm25`/`hyde-bm25`/`hyde-cosine` → count in final |

14. **API: `GET /api/metrics/rag`.** Auth-token protected by the existing middleware (consistent with everything else under `/api/`). Response:
    ```json
    {
      "aggregates": {
        "total_duration_ms": {"avg": 123.4, "p50": 110, "p95": 350},
        "hyde_duration_ms":  {...},
        "final_chunks_returned": {...},
        ...
      },
      "recent_events": [ /* last N ring-buffer entries, newest first */ ]
    }
    ```
    Default collection ON, exposure ON. UI consumer deferred (follow-up change).

15. **New package: `internal/rag/metrics/`** with `Recorder` interface (one method: `Record(MetricsEvent)`) and `RingRecorder` impl (fixed-capacity, thread-safe, overwrites oldest). **Why:** clean seam for a future `SQLiteRecorder` without disturbing call sites. A nil `Recorder` means "don't record" — tests pass nil by default.

### Eval suite

16. **10 queries in `internal/rag/testdata/eval_queries.json`**, categorized:
    - **3 purely semantic** (CV metawords class): `"curriculum vitae software engineer experience"`, `"professional background work history"`, `"achievements accomplishments impact"`.
    - **3 lexical** (verbatim terms in indexed docs): `"AWS S3 bucket lifecycle policy"`, `"kubectl describe pod error"`, `"EKS node group autoscaling"`.
    - **2 mixed**: `"container orchestration scheduling"`, `"distributed storage object versioning"`.
    - **2 edge**: `"hire me"` (ultra-short semantic, 2 tokens); `"expérience professionnelle"` (cross-language for CV content).

17. **Ground truth schema (example)** — labels filled in manually by the user as a gate before apply:
    ```json
    {
      "id": "q01",
      "query": "curriculum vitae software engineer experience",
      "category": "semantic",
      "ground_truth_chunk_ids": ["<doc_id>:<chunk_idx>", "..."],
      "notes": "User fills in after inspecting GET /api/knowledge"
    }
    ```
    **Manual gate (flagged for the user):** the proposal lands with an empty `ground_truth_chunk_ids` array per query; apply-phase stalls until the user labels them. The user inspects their actual indexed content via `GET /api/knowledge` and fills the IDs.

18. **Scoring harness: `go test -tags=eval ./internal/rag/...`.** Computes precision@5 for (baseline, HyDE-on) per query; reports per-query delta and overall delta. **The test FAILS only when HyDE regresses a lexical query whose baseline BM25 result was correct** — i.e. we hard-fail the case where adding HyDE *broke* something BM25 already solved. Semantic/mixed/edge cases are reported as soft metrics (stdout summary) but do not fail the test until we have enough labeled data to trust the numbers. **Why:** lexical queries are the one class where we *know* BM25 is right; any regression there is a HyDE bug. Semantic/mixed are where HyDE is *supposed* to help, and soft-reporting lets us iterate on prompt/weights without red builds.

### Docs sweep (C3)

19. **Update targets: `README.md`, `docs/CONFIG.md`, `DAIMON.md`, `TESTS.md`.** Scope for each:
    - `README.md` — mention `rag.hyde` in the features list; mention `daimon web` subcommand; mention knowledge tab under Web UI.
    - `docs/CONFIG.md` — add the three missing blocks: `rag.embedding`, `rag.retrieval`, `rag.hyde`. Short reference format, matches existing sections.
    - `DAIMON.md` — AI-context source of truth: add the new `RAGHydeConf` shape, the HyDE architectural decision (Option B / RRF), the metrics endpoint, and the eval suite.
    - `TESTS.md` — add the eval-tag invocation (`go test -tags=eval ./internal/rag/...`) and the ground-truth labeling gate.

20. **NOT in scope for this change:** `CHANGELOG.md` entry (follow-up), doc rewrites, tutorials, migration guides beyond a single "Upgrade notes" paragraph.

## 3. Files To Change

### C1 — HyDE retrieval (additive, opt-in)

| File | New/Modified | Purpose | Lines (est.) |
|---|---|---|---|
| `internal/rag/hyde.go` | new | Pure helpers: `RRFMerge(lists [][]string, k int) map[string]float64`, `EnsembleEmbed(hyp, raw []float32, queryWeight float64) []float32`, `Provenance(...)`. Exported for unit tests. | ~120 |
| `internal/rag/hyde_test.go` | new | Table-driven tests for RRF math, ensemble normalization, dedup. | ~180 |
| `internal/rag/config.go` | modified | Mirror `RAGHydeConf`; extend `ApplyRAGDefaults` with `HypothesisTimeout=10s`, `QueryWeight=0.3`, `MaxCandidates=20`. | +25 |
| `internal/config/config.go` | modified | Add canonical `RAGHydeConf`; add `Hyde RAGHydeConf` field on `RAGConfig`; extend `ApplyDefaults` for the same field set. | +30 |
| `internal/agent/agent.go` | modified | Add fields `ragHydeConf config.RAGHydeConf` + `ragHypothesisFn func(ctx, string) (string, error)`; add `WithHydeConf` setter mirroring `WithRAGRetrievalConf` (~line 292). | +30 |
| `internal/agent/loop.go` | modified | Insert HyDE branch in the existing RAG block (lines 128-151). Flow: if enabled AND `ragHypothesisFn != nil` → generate hypothesis (respect `HypothesisTimeout`), embed hypothesis + raw, ensemble + normalize, run 3 SearchChunks calls in sequence (raw, hypothesis-as-FTS with ensembleVec, raw-with-rawVec redundant with first — collapse to 2 calls: raw-BM25 and hyde-BM25+cosine), RRF merge, sort, slice to `ragMaxChunks`. On any failure → fall through to current path, log warn. | +80 |
| `internal/agent/loop_hyde_test.go` | new | Integration tests at loop level: mock hypothesisFn + mock embedFn + in-memory rag store; assert provenance, failure-path fallthrough, timeout behavior. | ~200 |
| `cmd/daimon/rag_wiring.go` | modified | Add `buildHypothesisFn(prov, modelOverride)` mirroring `buildSummaryFn`; resolve model chain `hyde.Model → ragCfg.SummaryModel → ""`; in `wireRAG`, after `WithRAGStore`, call `ag.WithHydeConf(ragCfg.Hyde, hypothesisFn)` when `ragCfg.Hyde.Enabled`. | +60 |
| `internal/web/handler_config.go` | modified | Extend `patchRAG` with `Hyde *config.RAGHydeConf`; merge field-by-field in the PUT handler (mirror the existing Retrieval block at lines 205-216). | +25 |
| `internal/web/handler_config_test.go` | modified | Add T-hyde-patch and T-hyde-preserves-unspecified (mirror T15/T16 from rag-retrieval-precision). | +80 |

### C2 — Retrieval metrics

| File | New/Modified | Purpose | Lines (est.) |
|---|---|---|---|
| `internal/rag/metrics/metrics.go` | new | `MetricsEvent` struct, `Recorder` interface, `RingRecorder` impl (fixed capacity, mutex, Snapshot() for API). | ~130 |
| `internal/rag/metrics/metrics_test.go` | new | Ring wrap-around, concurrent Record safety, Snapshot ordering (newest first), aggregate math (avg / p50 / p95) via reused helper. | ~160 |
| `internal/rag/metrics/aggregate.go` | new | Pure `aggregate(values []float64) Aggregate{Avg, P50, P95}`. Unit-testable in isolation. | ~50 |
| `internal/agent/agent.go` | modified | Add `ragMetricsRec metrics.Recorder` field + `WithRAGMetricsRecorder` setter. Nil recorder means "no-op". | +15 |
| `internal/agent/loop.go` | modified | Instrument the RAG block: build a `metrics.MetricsEvent` struct over the block, stamp each substep's timing, `Record` at the end. Guarded by `a.ragMetricsRec != nil`. | +40 |
| `internal/web/handler_metrics_rag.go` | new | `handleGetRAGMetrics(w, r)` — reads `deps.RAGMetricsRecorder.Snapshot()`, computes aggregates, writes JSON. | ~90 |
| `internal/web/handler_metrics_rag_test.go` | new | GET /api/metrics/rag returns aggregates+events; auth required; empty recorder returns empty shape. | ~120 |
| `internal/web/server.go` | modified | Register `s.mux.HandleFunc("GET /api/metrics/rag", s.handleGetRAGMetrics)` alongside the existing `/api/metrics` route (line 244). | +1 |
| `internal/web/server.go` | modified | Add `RAGMetricsRecorder metrics.Recorder` to `ServerDeps`. | +3 |
| `cmd/daimon/rag_wiring.go` | modified | When RAG enabled, construct `rec := metrics.NewRingRecorder(200)`, `ag.WithRAGMetricsRecorder(rec)`, return it via `RAGWiring.MetricsRecorder` so the web server can wire it in. | +10 |
| `cmd/daimon/main.go` (or wiring spot) | modified | Pass `RAGWiring.MetricsRecorder` into `ServerDeps.RAGMetricsRecorder`. | +2 |

### C3 — Docs sweep

| File | New/Modified | Purpose | Lines (est.) |
|---|---|---|---|
| `README.md` | modified | Add HyDE + metrics endpoint + knowledge tab + `daimon web` subcommand to features/quick-start sections. | +40 |
| `docs/CONFIG.md` | modified | Add `rag.embedding`, `rag.retrieval`, `rag.hyde` reference blocks (YAML shape + field semantics + defaults). | +90 |
| `DAIMON.md` | modified | Update the RAG architecture section: HyDE mode B via 3-way RRF, metrics endpoint, eval suite location, `RAGHydeConf` shape, opt-in default. | +60 |
| `TESTS.md` | modified | Add eval-suite section: `go test -tags=eval`, manual ground-truth labeling gate, scoring semantics (precision@5, hard-fail lexical regressions only). | +30 |

## 4. Behavior Changes

### HyDE OFF (default, every existing deployment on upgrade)

Identical to today. Agent loop runs the RAG block at `loop.go:128-151`, calls `SearchChunks(ctx, query, queryVec, searchOpts)` once with raw query + raw query vector, hands results to `buildSystemPrompt`. Metrics recorder, if wired, logs a single `MetricsEvent` with `hyde_enabled=false` and provenance `{raw-bm25: N}` — this is the baseline signal we'll compare HyDE turns against.

### HyDE ON

```
loop.go RAG block
  queryText = msg.Content.TextOnly()
  rawVec = ragEmbedFn(ctx, queryText)                                     // (1) existing
  start_total = now()

  if ragHypothesisFn != nil && ragHydeConf.Enabled:
      hctx = context.WithTimeout(ctx, ragHydeConf.HypothesisTimeout)
      t_hyp = now()
      hypText, err = ragHypothesisFn(hctx, queryText)                     // (2) new LLM call
      t_hyp_ms = ms_since(t_hyp)

      if err != nil OR hypText == "":
          log.Warn("hyde: hypothesis failed, falling through", err)
          goto BASELINE_PATH

      t_emb = now()
      hypVec = ragEmbedFn(ctx, hypText)                                    // (3) new embed call
      ensembleVec = normalize( (1 - query_weight)*hypVec + query_weight*rawVec )
      t_emb_ms = ms_since(t_emb)

      if magnitude(ensembleVec) < epsilon:                                 // zero-vector guard
          log.Warn("hyde: ensemble embed collapsed to zero, falling through")
          goto BASELINE_PATH

      opts_hyde = SearchOptions{Limit: ragHydeConf.MaxCandidates, ...retrieval knobs}
      opts_raw  = SearchOptions{Limit: ragHydeConf.MaxCandidates, ...retrieval knobs}

      rawBM25     = SearchChunks(ctx, queryText, rawVec, opts_raw)         // (4a) list A
      hydeResults = SearchChunks(ctx, hypText,   ensembleVec, opts_hyde)   // (4b) list B — FTS on hypothesis, cosine on ensemble

      // Each call internally yields ranked results where cosine path (if embeddings
      // present) has already resorted by cosine desc. We extract 3 conceptual lists:
      //   L_raw_bm25    = rawBM25 by bm25 rank
      //   L_hyde_bm25   = hydeResults by bm25 rank (before store's internal rerank)
      //   L_hyde_cosine = hydeResults by cosine rank
      // In practice the store returns a single merged list per call. To get
      // L_hyde_bm25 and L_hyde_cosine separately we either (a) extend SearchResult
      // with a CosineScore field alongside the existing Score so we can re-sort at
      // the agent layer, OR (b) call SearchChunks twice with the cosine-rerank path
      // toggled. Implementation detail: (a) is cheaper — one SQL round-trip — and
      // the field is already semantically needed for provenance.

      merged = RRFMerge([L_raw_bm25, L_hyde_bm25, L_hyde_cosine], k=60)
      merged = sortByRRFScoreDesc(merged)
      final  = merged[:ragMaxChunks]
      provenance = track(final, [L_raw_bm25, L_hyde_bm25, L_hyde_cosine])
  else:
      BASELINE_PATH:
      final = SearchChunks(ctx, queryText, rawVec, searchOpts)[:ragMaxChunks]
      provenance = {raw-bm25: len(final)}

  if ragMetricsRec != nil:
      ragMetricsRec.Record(MetricsEvent{
          timestamp: now(),
          query: truncateRunes(queryText, 80),
          total_duration_ms: ms_since(start_total),
          hyde_enabled: (hypText != ""),
          hyde_duration_ms: t_hyp_ms,
          hyde_embed_ms: t_emb_ms,
          bm25_hits: len(L_raw_bm25),
          cosine_hits: len(L_hyde_cosine),
          neighbors_expanded: countNeighbors(final),
          threshold_rejected_*: (if exposed by store — see §Open),
          final_chunks_returned: len(final),
          provenance_breakdown: provenance,
      })

  ragResults = final
```

### RRF merge pseudocode (reference — this is `rag.RRFMerge`)

```go
// k defaults to 60 (Cormack et al. 2009). lists is rank-ordered; position = rank.
func RRFMerge(lists [][]string, k int) map[string]float64 {
    out := map[string]float64{}
    for _, list := range lists {
        for rank, chunkID := range list {
            out[chunkID] += 1.0 / float64(k + rank + 1)  // rank+1 because 1-indexed
        }
    }
    return out
}
```

Provenance tracker: for each final chunkID, record which lists contained it. The metrics event stores aggregates like `{"raw-bm25": 3, "hyde-bm25": 2, "hyde-cosine": 4, "raw-bm25+hyde-cosine": 2}`.

## 5. Configuration Surface

### New YAML block (`rag.hyde.*`)

```yaml
rag:
  enabled: true
  # ... existing fields ...
  hyde:
    enabled: false              # opt-in; default false globally
    model: ""                   # empty → falls back to rag.summary_model → main provider
    hypothesis_timeout: 10s     # LLM call deadline; fall through on timeout
    query_weight: 0.3           # weight of RAW query in ensemble (hypothesis gets 1 - this)
    max_candidates: 20          # top-N per list before RRF merge
```

### Go struct (canonical, `internal/config/config.go`)

```go
// RAGHydeConf configures the HyDE (Hypothetical Document Embeddings) pass.
// All fields default to zero/off; users opt in by setting Enabled=true.
// YAML key: rag.hyde
type RAGHydeConf struct {
    Enabled           bool          `yaml:"enabled"            json:"enabled"`
    Model             string        `yaml:"model,omitempty"    json:"model,omitempty"`
    HypothesisTimeout time.Duration `yaml:"hypothesis_timeout" json:"hypothesis_timeout"` // default 10s
    QueryWeight       float64       `yaml:"query_weight"       json:"query_weight"`       // default 0.3
    MaxCandidates     int           `yaml:"max_candidates"     json:"max_candidates"`     // default 20
}
```

Mirrored in `internal/rag/config.go` with identical field set (same dual-location pattern as `RAGRetrievalConf` today). `ApplyDefaults` / `ApplyRAGDefaults` fill in the non-bool defaults when zero-valued.

### patchRAG extension (`internal/web/handler_config.go`)

```go
type patchRAG struct {
    Embedding *config.RAGEmbeddingConf `json:"embedding,omitempty"`
    Retrieval *config.RAGRetrievalConf `json:"retrieval,omitempty"`
    Hyde      *config.RAGHydeConf      `json:"hyde,omitempty"`   // NEW — mandatory
}
```

Merge block (mirrors Retrieval at lines 205-216):

```go
if patch.RAG != nil && patch.RAG.Hyde != nil {
    p := *patch.RAG.Hyde
    // Bool: always copy (absent hyde.enabled is preserved by the outer pointer nil check).
    merged.RAG.Hyde.Enabled = p.Enabled
    if p.Model != "" {
        merged.RAG.Hyde.Model = p.Model
    }
    if p.HypothesisTimeout != 0 {
        merged.RAG.Hyde.HypothesisTimeout = p.HypothesisTimeout
    }
    if p.QueryWeight != 0 {
        merged.RAG.Hyde.QueryWeight = p.QueryWeight
    }
    if p.MaxCandidates != 0 {
        merged.RAG.Hyde.MaxCandidates = p.MaxCandidates
    }
}
```

(`Enabled` is a bool with zero = `false`; disabling via PUT requires sending `{"hyde": {"enabled": false, ...}}` with at least one other non-zero field, or a nil-out pattern we already accept elsewhere. If this turns out to be a UX problem we'll revisit after first user feedback — not in this change's scope.)

## 6. Test Plan (Strict TDD — tests land BEFORE implementation)

All tests table-driven where the axis admits it. `testing.T`, no non-stdlib assertion libraries.

### Unit — HyDE pure helpers (`internal/rag/hyde_test.go`)

| # | Case | Setup | Assert |
|---|---|---|---|
| T1 | `RRFMerge` with disjoint lists | `[["a","b"], ["c","d"]]`, k=60 | All four chunks present; `a > c > b > d` by score (ranks 1,1,2,2 → a=b=1/61, c=d=1/61; order broken by list-encounter). |
| T2 | `RRFMerge` with overlap | `[["a","b","c"], ["b","d"]]`, k=60 | `b` appears once; score = 1/62 + 1/61 > any single-list score; final order `b, a, c, d`. |
| T3 | `RRFMerge` with three lists | raw-bm25, hyde-bm25, hyde-cosine all containing `x` at different ranks | `x` RRF score = sum of three reciprocals. |
| T4 | `RRFMerge` empty input | `[]` | Returns empty map, no panic. |
| T5 | `RRFMerge` one empty list | `[["a"], []]` | `a` present; empty list contributes nothing. |
| T6 | `EnsembleEmbed` happy path | `hyp=[1,0,0]`, `raw=[0,1,0]`, `queryWeight=0.3` | Result = normalize(0.3*[0,1,0] + 0.7*[1,0,0]) = normalize([0.7, 0.3, 0]). |
| T7 | `EnsembleEmbed` dimension mismatch | `hyp` len 3, `raw` len 4 | Returns error or zero vector (spec: error — apply phase decides). |
| T8 | `EnsembleEmbed` both zero | `hyp=[0,0,0]`, `raw=[0,0,0]` | Returns zero vector; caller guard detects and falls through. |
| T9 | `EnsembleEmbed` queryWeight bounds | weight=0.0 → pure hypothesis; weight=1.0 → pure raw | Correct in both limits; no NaN. |
| T10 | `Provenance` single-source chunk | chunk `x` only in hyde-cosine | `provenance["hyde-cosine"] == 1`. |
| T11 | `Provenance` multi-source chunk | chunk `x` in raw-bm25 AND hyde-cosine | Recorded with combined key or aggregated per list (design choice — assert the schema decision). |

### Config + patchRAG (`internal/web/handler_config_test.go`, `internal/config/config_test.go`)

| # | Case | Setup | Assert |
|---|---|---|---|
| T12 | `ApplyDefaults` sets HyDE defaults | Zero-valued `RAGConfig` | `Hyde.HypothesisTimeout == 10*time.Second`, `QueryWeight == 0.3`, `MaxCandidates == 20`; `Enabled == false`. |
| T13 | Defaults do NOT enable HyDE | Zero-valued `RAGConfig.Hyde.Enabled` | Stays `false` after `ApplyDefaults`. |
| T14 | PUT /api/config accepts `rag.hyde` | PUT body `{"rag":{"hyde":{"enabled":true,"query_weight":0.5}}}` | GET returns enabled=true, query_weight=0.5; config file on disk contains both. |
| T15 | PUT /api/config preserves unspecified hyde fields | Start with `{enabled:true, model:"gemini-2.5-flash", query_weight:0.5}`; PUT only `{max_candidates: 30}` | All four fields end up set correctly (enabled/model preserved, max_candidates updated, query_weight preserved). **REGRESSION GUARD — mirrors T16 in rag-retrieval-precision.** |
| T16 | PUT /api/config with missing `hyde` key leaves stored hyde intact | Start with non-default hyde; PUT `{"rag":{"embedding":{...}}}` (no hyde key) | Stored `Hyde` is unchanged. |

### Metrics (`internal/rag/metrics/metrics_test.go`)

| # | Case | Setup | Assert |
|---|---|---|---|
| T17 | RingRecorder wraps at capacity | Capacity=3; Record 5 events | Snapshot returns last 3, newest first. |
| T18 | Concurrent Record safe | 100 goroutines × 10 Records | No data race (`-race`); Snapshot length == capacity; all events well-formed. |
| T19 | Empty recorder snapshot | Never Recorded | Snapshot returns empty slice (not nil or panic). |
| T20 | Aggregate math | values `[1,2,3,4,5,6,7,8,9,10]` | avg=5.5, p50=5 or 6 (document chosen convention), p95=9 or 10. |
| T21 | Aggregate on empty | `[]` | Returns zero struct (avg=0, p50=0, p95=0); no panic. |
| T22 | Query truncation is rune-safe | 200-rune multi-byte query (e.g. Japanese) | Truncated field has ≤ 80 runes, not 80 bytes; no `\xff\xfd` at the edge. |

### Integration — agent loop with HyDE (`internal/agent/loop_hyde_test.go`)

Fixture: in-memory SQLite store seeded with ~15 chunks spanning 3 docs; mock `ragEmbedFn` (deterministic: returns a vector derived from hashing the text); mock `ragHypothesisFn` (returns a canned hypothesis per input query); mock Recorder.

| # | Case | Setup | Assert |
|---|---|---|---|
| T23 | HyDE disabled → baseline path | `Hyde.Enabled=false` | Exactly one SearchChunks call; metrics event records `hyde_enabled=false`; provenance `{raw-bm25: N}`. |
| T24 | HyDE enabled, happy path | `Hyde.Enabled=true`, hypothesisFn returns canned text | Two SearchChunks calls; final results contain at least one chunk attributed to `hyde-cosine` or `hyde-bm25` in provenance. |
| T25 | Hypothesis fn error → fallthrough | hypothesisFn returns `errors.New("provider down")` | One SearchChunks call (baseline); metrics event `hyde_enabled=false`; warn log emitted. |
| T26 | Hypothesis fn timeout | `HypothesisTimeout=5ms`; hypothesisFn sleeps 50ms | Context cancels; fallthrough to baseline; `hyde_duration_ms` ≤ timeout + jitter. |
| T27 | Hypothesis fn returns empty | hypothesisFn returns `""` | Fallthrough to baseline; `hyde_enabled=false` in event. |
| T28 | Zero-vector ensemble | Mock embedFn returns `[0,0,0]` for both raw and hypothesis | Fallthrough to baseline; warn log `"ensemble embed collapsed"`. |
| T29 | Dedup in merged result | Chunk `x` present in all three lists | Appears exactly once in final; provenance records all three sources. |
| T30 | Top-K respected | `ragMaxChunks=5`; RRF merge yields 12 unique chunks | Final has exactly 5 entries, sorted by RRF score desc. |
| T31 | Metrics event recorded on every turn | Call loop.processTurn twice | Recorder has 2 events. |

### Metrics API (`internal/web/handler_metrics_rag_test.go`)

| # | Case | Setup | Assert |
|---|---|---|---|
| T32 | GET /api/metrics/rag empty | Fresh recorder, no events | Returns `{aggregates: {...zeroes...}, recent_events: []}`; 200 OK. |
| T33 | GET /api/metrics/rag with events | Recorder has 10 events | Aggregates computed across all 10; recent_events length == 10, newest first. |
| T34 | Auth required | Request without Bearer token | 401. |
| T35 | Ring cap respected | Recorder cap=3, but seeded 7 events | recent_events length == 3 (oldest four dropped). |

### Eval (`internal/rag/eval_test.go` with `//go:build eval` tag)

| # | Case | Setup | Assert |
|---|---|---|---|
| T36 | Scoring harness reads eval_queries.json | Empty labels | Test skips with clear message: "ground truth not labeled; run manual labeling gate before eval". |
| T37 | precision@5 regression guard on lexical | Labeled lexical query where baseline P@5=1.0 | If HyDE-on P@5 < 1.0 for this query → test FAILS with per-query delta report. |
| T38 | Soft reporting on semantic | Any semantic query | Test logs baseline vs HyDE P@5, delta, but does not fail the run. |

**Manual labeling gate (documented in TESTS.md):** user runs `curl -s localhost:<port>/api/knowledge | jq` to inspect their indexed content, fills in `ground_truth_chunk_ids` per eval query, commits `testdata/eval_queries.json`. Only then does `go test -tags=eval` produce meaningful numbers.

## 7. Rollout

### Backward compatibility guarantees

- `Hyde.Enabled` defaults to `false` globally. Every existing deployment sees zero behavior change on upgrade.
- `HypothesisTimeout`, `QueryWeight`, `MaxCandidates` defaults (10s / 0.3 / 20) only kick in WHEN `Enabled=true`.
- No DB migration, no re-embed, no re-index. HyDE operates entirely on the existing `document_chunks` + `document_chunks_fts` tables.
- `patchRAG` extension is additive: PUTs that only send `embedding` or `retrieval` keep working identically.
- Metrics recorder is nil-safe: loop instrumentation only records when `ragMetricsRec != nil`, and `wireRAG` only constructs a recorder when RAG is enabled.
- `/api/metrics/rag` is a new endpoint — no existing route or response changes.

### Manual upgrade note (for the user's own `~/.daimon/config.yaml`)

Append at the end of the `rag:` block:

```yaml
  hyde:
    enabled: true
    # model: ""                   # inherit from rag.summary_model
    # hypothesis_timeout: 10s
    # query_weight: 0.3
    # max_candidates: 20
```

Restart `daimon`. No config migration tool — this is a one-line manual edit by design (per frozen decision #8). The `rag.summary_model` fallback means the operator does not need to set `rag.hyde.model` unless they want HyDE to use a different model than summarization.

### Rollback

- **Config rollback (cheap):** set `rag.hyde.enabled: false` (or remove the block). Immediate behavior reversion. Existing chats continue using the baseline retrieval path.
- **Full git revert:** three commits, single PR — revert the PR. No schema debt, no migration debt. Configs mentioning `rag.hyde.*` will decode cleanly under old code (unknown keys are ignored by `yaml` decoding under `strict: false`, which is the current default — verify in apply, but this matches rag-retrieval-precision's rollback behavior).

## 8. Verification

Quality gates per golang-pro rules. All must pass before PR merges.

```bash
# Tests, race detector, on affected packages.
go test -race ./internal/rag/... ./internal/agent/... ./internal/web/...

# Eval suite (requires labeled testdata/eval_queries.json).
go test -tags=eval ./internal/rag/...

# Static analysis.
go vet ./...
golangci-lint run

# Coverage on new code must be ≥ 80%.
go test -cover ./internal/rag/metrics/... ./internal/rag/... ./internal/agent/... -coverprofile=cov.out
go tool cover -func=cov.out | grep -E "hyde|metrics"
```

Manual smoke (pre-merge, single 10-minute pass):

1. `daimon web`, append `rag.hyde.enabled: true` to config, restart.
2. Ingest a CV document via the knowledge tab.
3. Query `"CV curriculum vitae resume"` — confirm at least one CV chunk surfaces in the agent's RAG section (UI or logs).
4. `curl -s -H "Authorization: Bearer <token>" http://localhost:<port>/api/metrics/rag | jq '.recent_events[0]'` — confirm the event has `hyde_enabled=true` and provenance_breakdown contains at least one HyDE-sourced hit.
5. Flip `enabled: false`, restart, repeat step 3 — confirm zero CV hits (baseline reproduces the failure).

## 9. Out of Scope (explicit, do not creep)

- **Multi-sample HyDE** (generate N hypotheses, average embeddings). Deferred until single-sample data in metrics + eval suite shows it's worth the 2×–4× latency hit.
- **Query rewriting** as a complement to HyDE (rewrite raw query into multiple query variants, embed each, merge). Different technique, different config, different cost profile. Separate change.
- **Pure cosine-only path** via `SearchOptions.SkipFTS`. Three-way RRF (which uses hypothesis text as FTS query) is cheaper and delivers equivalent recall on the tested failure cases. Revisit only if a future failure case proves it's needed.
- **Settings UI exposure** of `rag.hyde.*`. The PUT endpoint accepts it; UI surface is a follow-up change after we see operator demand.
- **CHANGELOG.md entry.** Follow-up change; keeps this PR surgical.
- **Chunker polymorphism** (runtime-selectable chunker). Separate concern, unrelated to HyDE. Owned by a future Phase D.
- **HyDE caching** (memoize hypothesis per query). Hypothesis variance is part of the benefit; caching reduces it. Can be revisited with metrics data.
- **Persistent metrics storage** (SQLite ring). Ring buffer is in-memory by design; persistence is a follow-up once we have a UI consumer.
- **Dashboard UI for `/api/metrics/rag`.** Endpoint ships; UI is a separate change.

## 10. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `patchRAG` allow-list regression — `Hyde` field silently dropped on PUT | High if forgotten | Medium | T14/T15 lock the round-trip. mirrors rag-retrieval-precision T16. Include in task checklist as a standalone item, not folded into generic "wire config". |
| Hypothesis prompt produces low-quality text for certain query classes (e.g. ultra-short queries) | Medium | Low (eval suite will surface) | Eval q07 "hire me" + q10 "achievements accomplishments impact" cover this. Prompt is version-controlled; iteration is cheap. |
| Latency spike on first HyDE-enabled turn (cold LLM connection) | Medium | Low | `HypothesisTimeout` (10s default) bounds the worst case; fallthrough path means no hard failure. Metrics expose per-turn `hyde_duration_ms` for tuning. |
| Dual-location config struct drift (canonical `config.RAGHydeConf` vs local `rag.RAGHydeConf`) — fields added in one not the other | Medium | Medium | Fold both into the same apply-phase task. Add a compile-time assertion `var _ = config.RAGHydeConf(rag.RAGHydeConf{})` in `cmd/daimon/rag_wiring.go` if the conversion becomes non-trivial. |
| Zero-vector ensemble embedding triggers division-by-zero during normalize | Low | Low | T28 covers explicitly. `NormalizeEmbedding` already handles magnitude=0 (returns zero vector); the agent loop's magnitude check catches this before SearchChunks runs. |
| RRF k=60 is wrong for Daimon's candidate list sizes (≤20) | Low | Low | k=60 is the published default across TREC evaluations. If eval shows worse results we expose `rag.hyde.rrf_k` in a follow-up (not now — keep the surface small). |
| Metrics ring buffer grows memory unexpectedly under heavy contention | Low | Low | Fixed capacity = N=200 events × ~500 bytes/event ≈ 100 KB. Bounded by design. |
| `/api/metrics/rag` leaks user queries (80-rune prefix is enough to identify a user's topic) | Medium | Medium | Endpoint is auth-token protected (same as every other `/api/` route). Truncation bounds exposure. Future: add `rag.metrics.query_logging: false` knob if operators object. **Flag for user decision, not blocker.** |
| Eval harness false-fails on a flaky lexical test | Low | Medium | Only the lexical-regression-with-correct-baseline case is hard-fail; semantic/mixed are soft. If a lexical query is flaky it probably means the test data is wrong, which is a review issue not a HyDE issue. |
| Hypothesis generation drains provider rate limit | Medium | Medium | Uses the same provider + fallback chain as the chat path; if chat works, hyde works; if chat's rate-limited, chat falls back to fallback provider and hyde falls through cleanly. |
| The user's `rag.summary_model` is set to a slow/expensive model and hyde picks it up silently | Medium | Low | Resolution logged at `slog.Info` during `wireRAG` — operator sees the resolved model at startup. Document in CONFIG.md. |

## 11. Success Criteria

- [ ] Hypothesis fn fires in `loop.go` under `ragHydeConf.Enabled=true`; baseline path untouched when disabled.
- [ ] RRF merges three lists (raw-BM25, hyde-BM25, hyde-cosine); provenance tracked per final hit.
- [ ] `rag.hyde` YAML block round-trips via PUT /api/config (T14/T15 green).
- [ ] `GET /api/metrics/rag` returns aggregates + last-N events; auth-protected.
- [ ] Ring buffer caps at 200 events, thread-safe under `-race`.
- [ ] Hypothesis fn failure / timeout / empty output / zero ensemble → falls through to baseline with a warn log; never fails retrieval.
- [ ] Eval harness runs under `go test -tags=eval`; hard-fails on lexical regressions only.
- [ ] README, CONFIG, DAIMON, TESTS updated with the three missing blocks.
- [ ] `go vet ./...`, `golangci-lint run`, and `go test -race` all green on `internal/rag/...`, `internal/agent/...`, `internal/web/...`.
- [ ] Coverage ≥ 80% on `internal/rag/metrics/` and the HyDE helpers.
- [ ] Manual smoke: CV query returns CV chunks with HyDE on; fails to return CV chunks with HyDE off (reproduces the baseline regression for compare).

## 12. Commit Split

Single branch `feat/rag-hyde`, single PR, three logical commits:

- **C1 — `feat(rag): HyDE retrieval — additive via 3-way RRF, opt-in`**
  Scope: `RAGHydeConf` (both locations) + `ApplyDefaults` + `patchRAG` + `WithHydeConf` + `loop.go` branch + `hyde.go`/`hyde_test.go` + `buildHypothesisFn` in `rag_wiring.go` + integration tests.

- **C2 — `feat(rag): retrieval metrics — in-memory ring buffer + /api/metrics/rag`**
  Scope: `internal/rag/metrics/` package + `WithRAGMetricsRecorder` setter + loop instrumentation + `handler_metrics_rag.go` + route registration + wiring in `rag_wiring.go` and `main.go` + handler tests.

- **C3 — `docs: catch up on v0.7.0 + v0.8 RAG blocks + knowledge tab + web subcommand`**
  Scope: README.md, docs/CONFIG.md, DAIMON.md, TESTS.md. No code changes.

Dependencies: C2 logically depends on C1 (loop instrumentation happens inside the RAG block that C1 rewrites). C3 is pure docs and depends on C1 + C2 landing. All three ship in the same PR.

## 13. Dependencies

- No new Go modules.
- No new external services.
- Uses existing provider abstraction (`provider.Provider.Chat`, `provider.EmbeddingProvider.Embed`) for hypothesis generation and embedding.
- Builds on `rag.embedding` (v0.7.0), `rag.retrieval` (rag-retrieval-precision), and `rag.summary_model` (existing).
- No DB schema change; no migration.
