# Audit — parallel-path inconsistencies

Conducted 2026-04-22 during the rag-hyde testing session after two dual-path
bugs were caught in rapid succession:
1. `main.go` vs `cmd/daimon/web_cmd.go` — `wireRAG` called in main, missing in web (fixed PR #2 commit `78e1484`).
2. `internal/agent/loop.go` (HyDE wired) vs `internal/rag/tools.go:195` — `search_docs` tool bypassed HyDE entirely (fixed in the follow-up commit).

## Summary

This audit found **1 confirmed wiring gap** (CRITICAL) and **1 likely allow-list gap** (MEDIUM). The critical finding is the same class as the two known fixes — a setter that exists on `Agent` but is never invoked in `wireRAG`, silently zeroing user config. The audit scope covered startup-path duality, retrieval/memory callsites, tool ↔ agent-loop parity, config PATCH allow-list, auth middleware, extractor routing, cost budgets, and failure fallthrough. Zones with no findings are explicitly noted rather than padded.

## Findings

### Finding #1 — RAG retrieval config never wired to agent loop — CRITICAL

**Zone**: 1, 3 (Startup path duality + tool/agent-loop parity)

**Evidence**:
- `cmd/daimon/rag_wiring.go:82` — `wireRAG()` calls `ag.WithRAGStore()` but NOT `ag.WithRAGRetrievalConf()` (as of the pre-fix commit).
- `internal/agent/agent.go:297-303` — `WithRAGRetrievalConf()` setter exists but is never invoked in any startup path.
- `internal/agent/loop.go` — loop reads `a.ragRetrievalConf`, which remained at zero-valued defaults, so every `SearchOptions` built in the loop silently used defaults regardless of user config.
- `cmd/daimon/main.go:494` & `cmd/daimon/web_cmd.go:323` — both call `wireRAG()` identically, so the bug affected BOTH startup paths.

**Effect on user**: `rag.retrieval.neighbor_radius`, `rag.retrieval.max_bm25_score`, and `rag.retrieval.min_cosine_score` set in `~/.daimon/config.yaml` were silently ignored during document retrieval. Searches always used zero-valued defaults (all features disabled), preventing user-tuned precision from taking effect. **Shipped in PR #2 commit `a5c967d` and remained dead for ~24h until this audit.**

**Proposed fix scope**: Small — one call after `WithRAGStore` in `wireRAG`: `ag.WithRAGRetrievalConf(rag.RAGRetrievalConf{...fields from ragCfg.Retrieval...})`. Plus a regression guard (source-scan test) to detect orphaned setters.

**Status**: Confirmed

### Finding #2 — Top-level RAG config fields missing from web PATCH allow-list — MEDIUM

**Zone**: 4 (Config allow-list coverage in `PUT /api/config`)

**Evidence**:
- `internal/web/handler_config.go:43-48` — `patchRAG` struct allow-lists only: `Embedding`, `Retrieval`, `Hyde`, `Metrics`.
- `internal/config/config.go:246-257` — `RAGConfig` includes additional top-level fields: `ChunkSize`, `ChunkOverlap`, `TopK`, `MaxDocuments`, `MaxChunks`, `MaxContextTokens`, `SummaryModel`.
- `handler_config.go:194-265` — only the four allow-listed sub-blocks are merged; others are silently preserved from stored config (the JSON decoder skips unknown fields at the HTTP layer).

**Effect on user**: If a future UI adds controls for `ChunkSize`, `TopK`, or `MaxContextTokens`, edits via `PUT /api/config` will be silently dropped. Changes would require command-line YAML edit + restart, with no error feedback.

**Proposed fix scope**: Small — expand `patchRAG` struct to include all user-editable RAG top-level fields. Only urgent if UI exposure is planned; otherwise defer until UI work begins.

**Status**: Likely (latent — unknown whether the UI currently exposes these)

## Zones with no findings

- **Zone 2 (retrieval/memory callsites)**: beyond the known `search_docs` HyDE-bypass (already fixed), all `SearchChunks` callers respect `SearchOptions`. `SearchMemory` is consistently routed.
- **Zone 5 (auth middleware)**: every `/api/*` handler goes through the auth middleware. The only exceptions are `/api/setup/*` (pre-configuration, correct) and static asset serving.
- **Zone 6 (ingestion pipeline routing)**: all ingestion paths route through `DocIngestionWorker`. Extractor chain is deterministic.
- **Zone 7 (cost budget)**: `agent.max_total_tokens` is enforced in the main chat call path; hypothesis generation (HyDE) and summary generation are not yet budgeted, but are short fixed-cost calls (flagged as known limitation, not a miss).
- **Zone 8 (failure fallthrough)**: HyDE failure semantics are consistent across `PerformHydeSearch` (post-fix) — all paths fall through to BM25-only, never escalate.

## Summary table

| # | Zone | Severity | Category | File | Effect |
|---|------|----------|----------|------|--------|
| 1 | 1, 3 | CRITICAL | Wiring gap | `cmd/daimon/rag_wiring.go:82` | Retrieval precision config silently ignored |
| 2 | 4 | MEDIUM | Allow-list | `internal/web/handler_config.go:43` | Future UI edits on top-level RAG fields silently dropped |

## Strategy for ongoing detection

The root cause pattern — "setter exists but never called in wiring" — can be detected at four levels, ranked by leverage:

1. **Compile-time assertion for config mirror structs** (highest leverage, lowest cost).
   Extend the existing pattern at `cmd/daimon/rag_wiring.go:23-24` (`var _ = config.X(rag.X{})`) to every major config sub-block (Agent, RAG, Tools, Memory, Limits). Build fails if `config.*` and mirror `*.Config` diverge. Catches the dual-mirror drift class of bug.

2. **Source-scan regression guards** (low cost, brittle against renames but good for now).
   Add tests like the new `cmd/daimon/rag_wiring_test.go::TestWireRAG_PropagatesAllConfig` that scan `rag_wiring.go` for required setter-call strings. Any future `With*Conf` addition triggers a test update, which surfaces the contract requirement. Already shipped for wireRAG.

3. **Linter rule for orphaned setter methods** (best long-term answer).
   Custom `go vet` analyzer OR `golangci-lint` plugin that enumerates all `With*()` methods on `*Agent` (and similar DI targets) and scans startup packages (`cmd/*`) for calls to each. Warn if any setter is defined but never invoked anywhere. Implementation path: ~200 LOC Go analyzer, integrated into existing `golangci-lint run` in CI.

4. **Startup-path symmetry integration test** (catches dual-path drift).
   New file `cmd/daimon/startup_symmetry_test.go` that invokes minimal versions of both `main`-path and `web`-subcommand-path wiring side-by-side, diffs the resulting agent state and tool registry. Also shipped as `TestStartupPaths_BothCallWireRAG` in `rag_wiring_test.go`.

5. **PATCH coverage test** (specific to `PUT /api/config`).
   For each `patch*` struct in `handler_config.go`, verify that every user-editable top-level field in the corresponding `config.*` struct is represented. Add: `TestPatchRAG_CoversAllEditableFields`, `TestPatchAgent_CoversAllEditableFields`, etc. Low cost, catches Finding #2's class.

**Recommended order**: (2) is already shipped; add (1) and (5) in a focused follow-up change (~1 day); (3) and (4) as longer-term tooling investments if the pattern recurs more than twice in a quarter.
