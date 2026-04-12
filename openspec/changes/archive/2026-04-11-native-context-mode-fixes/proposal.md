# Proposal: Native Context Mode — Quality Fixes

## Intent

Review of `native-context-mode` (commits `cafea93 → 02b2ef5`) found 5 HIGH + 9 MEDIUM correctness bugs and 5 weak/missing tests. CI is green but hides swallowed errors, false type contracts, tautological assertions, and dead code. Raise the feature to production quality before Phase 6+ builds on it.

## Scope

### In Scope
- Fix HIGH bugs H1–H5 in `batch.go`, `loop.go`, `sandbox.go`.
- Fix MEDIUM bugs M1–M9 across filter, store, sandbox, batch.
- Strengthen T1–T3; add T4 coverage (PreApply/Apply, LIKE fallback, `IndexOutput` validation, decay math, Porter stemmer).
- Rename `Sandbox` → `BoundedExec` (gated: doc-only if rename cascades).
- Delete dead `preApplyFileRead`; accept `FileChunkSize` but log it as reserved.

### Out of Scope
- Phase 2 file chunking.
- Real process isolation (namespaces, cgroups, seccomp).
- Phase 7 provider catalog.
- Any public-API / user-config breakage.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `sandboxed-execution`: `Output` contract fixed, `combinedBuf` bounded, diagnostic error surfaced, naming/doc clarified.
- `batch-execution`: index error logged (not swallowed), UUID IDs, rune-safe preview.
- `output-indexing`: `IndexOutput` validates fields; `LIKE` search escapes `%`/`_`; async write from loop.
- `filter-system`: dead `preApplyFileRead` removed; `presummarized` marker prevents double-filtering PreApply shell output.
- `shell-tool`: real `exit_code` and sandbox `Truncated` propagated into the index.

## Approach

Five ordered groups; later groups consume earlier ones.

1. **Sandbox contract + buffer** (H4, H5, M5, M9): fix `Output` doc, wrap `combinedBuf` in `LimitedWriter`, add `exit_code`/`truncated` to `result.Meta`, name cleanup.
2. **Loop correctness** (H2, H3, M2, M6): read real exit code from meta, prefer sandbox truncated flag, set `presummarized` marker, move `IndexOutput` to a buffered async worker.
3. **Batch cleanup** (H1, M7, M8): `slog.Warn` instead of discarded `fmt.Errorf`, `uuid.New()` IDs, rune-safe preview truncation.
4. **Store hardening** (M3, M4): validate `IndexOutput` inputs; `ESCAPE '\\'` in `LIKE`.
5. **Filter cleanup + tests** (M1, T1–T4): drop dead code, then land test strengthening and new coverage.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/tool/sandbox.go` | Modified | H4/H5/M5/M9 — contract, bounded buffer, error kind, rename |
| `internal/tool/batch.go` | Modified | H1/M7/M8 |
| `internal/agent/loop.go` | Modified | H2/H3/M2/M6 |
| `internal/filter/filter.go` | Modified | M1/M2 |
| `internal/store/sqlitestore.go` | Modified | M3/M4 |
| `internal/tool/sandbox_test.go` | Modified | T1–T3 |
| New `*_test.go` | Added | T4 gaps |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| `combinedBuf` rework regresses output capture | Med | Prefer **LimitedWriter wrap** (Option A) over pipes+goroutines (Option B). Fall back to B only if head+tail guarantee breaks. |
| `Sandbox`→`BoundedExec` rename cascades | Low-Med | If >15 touch points or leaks to exported symbols, keep the doc fix and defer rename. |
| Async IndexOutput shutdown race | Med | Buffered channel + worker drained on `ctx.Done()`; add a drain test. |

## Rollback Plan

Each of the 5 groups lands as an independently revertible commit. No schema migration, no config break, no public-API change — rollback is `git revert` + rebuild. `preApplyFileRead` deletion is recoverable from git history.

## Dependencies

None external. Stays inside Go 1.26.1 / sqlite / existing deps.

## Success Criteria

- [ ] H1–H5 fixed with regression tests.
- [ ] M1–M9 fixed OR deferred with written justification in the verify report.
- [ ] T1–T3 replaced with real assertions; T4 gaps covered.
- [ ] `go build`, `go vet`, `go test -race`, `golangci-lint run` all green.
- [ ] No change to public tool schemas or user-facing config keys.
- [ ] `SandboxResult.Output` doc matches actual behavior.
