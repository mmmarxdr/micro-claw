# Tasks: native-context-mode-fixes

## Phase 1 — BoundedExec rename + Option A++ rework

- [x] 1.1 Create `internal/tool/bounded_exec.go`: define `ExecErrorKind` type with constants `ExecErrorNone/Start/Timeout/Killed/Other`; define `BoundedExecMetrics` (same fields as `SandboxMetrics` plus `ErrorKind ExecErrorKind`); define `BoundedExecResult` (same fields as `SandboxResult`); define `BoundedExec` struct with doc comment explicitly stating "byte-limit + timeout only; no filesystem/network/privilege isolation" (M9, D1, M5/addendum-B)
- [x] 1.2 Implement three-writer design in `BoundedExec.Run`: `headBuffer *bytes.Buffer` (plain, unbounded), `tailRing *ringBuffer` (ring buffer, `KeepLastN` lines), `byteCounter *countingWriter` (count-only, no sink); `sequentialWriter` wraps all three and ALWAYS returns `(len(p), nil)` — never propagates errors to `io.Copy` (H4, H5, D2)
- [x] 1.3 Implement `ringBuffer` helper (line-aware ring, keeps last N lines) in `bounded_exec.go` or a private `ring_buffer.go` under `internal/tool` (D2)
- [x] 1.4 Implement `ExecErrorKind` detection in `BoundedExec.Run` after `cmd.Run()` returns: check `err==nil` → `None`; `ctx.Err()==DeadlineExceeded` → `Timeout`; `errors.Is(err, exec.ErrNotFound)` or `os.IsNotExist(pathErr)` → `Start`; `exitErr.ProcessState != nil && !Exited()` → `Killed`; else → `Other`. Do NOT change `Run` return type (still returns `error=nil`) (M5, addendum-B)
- [x] 1.5 Update `internal/tool/batch.go` line 88: change `&Sandbox{` → `&BoundedExec{`; update comments on lines 20, 76, 109 (D1)
- [x] 1.6 Update `internal/filter/filter.go` line 63: change `sb := &tool.Sandbox{` → `sb := &tool.BoundedExec{`; update comment on line 49 and error comment on line 72; rename local var `sb` → `be` for clarity (D1)
- [x] 1.7 Rename `internal/tool/sandbox_test.go` → `internal/tool/bounded_exec_test.go`; update all `Sandbox{` → `BoundedExec{`, `SandboxMetrics{` → `BoundedExecMetrics{`, `SandboxResult` → `BoundedExecResult` references inside (D1)
- [x] 1.8 Delete `internal/tool/sandbox.go` (D1)
- [x] 1.9 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/tool/...` (infra)

## Phase 2 — ToolResult Meta keys + filter coherence

- [x] 2.1 In `filter.go:preApplyShell`, rename all new Meta keys to use `microagent/` prefix: set `result.Meta["microagent/exit_code"]`, `"microagent/truncated"` (`"true"/"false"`), `"microagent/presummarized"` (`"true"`), `"microagent/error_kind"` (`string(result.Metrics.ErrorKind)` when non-empty) (addendum-A, D3, D4, D5, M5)
- [x] 2.2 In `filter.go:Apply`, add early-return at top of function body (before the switch): `if result.Meta["microagent/presummarized"] == "true" { return result, Metrics{} }` (M2, D5)
- [x] 2.3 Delete `preApplyFileRead` function from `filter.go` (body + comment block); remove its `case "read_file":` branch from `PreApply` switch (M1)
- [x] 2.4 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/filter/... ./internal/tool/...` (infra)

## Phase 3 — Agent loop Meta propagation fixes

- [x] 3.1 In `loop.go` line 245, replace `filterMetrics.CompressedBytes < filterMetrics.OriginalBytes` with: `parseBoolMeta(result.Meta, "microagent/truncated")` where `parseBoolMeta` is a file-local helper that reads the key and falls back to filterMetrics comparison when key is absent (H3, D4)
- [x] 3.2 In `loop.go` line 246, replace `ExitCode: 0` with: read `result.Meta["microagent/exit_code"]` via `strconv.Atoi`, fallback to `0` on missing/invalid; remove the misleading comment `// No way to get actual exit code without changing tool interface` (H2, D3)
- [x] 3.3 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/agent/...` (infra)

## Phase 4 — Async IndexingWorker

- [x] 4.1 Create `internal/agent/indexing_worker.go`: define `IndexingWorker` with buffered channel `cap 256`, single goroutine draining to `outputStore.IndexOutput`; `Enqueue(output store.ToolOutput)` drops with `slog.Warn` when channel full; `Stop()` closes channel, drains remaining items with `2s` hard timeout (M6, D6)
- [x] 4.2 Add `indexingWorker *IndexingWorker` field to `Agent` struct in `agent.go` (D6)
- [x] 4.3 In `agent.New()`, wire `indexingWorker = NewIndexingWorker(outputStore)` when `outputStore != nil`; start worker in `Run()` after existing workers; stop in `Shutdown()` AFTER `channel.Stop()` (D6)
- [x] 4.4 In `loop.go` line 249, change sync `a.outputStore.IndexOutput(ctx, output)` to `a.indexingWorker.Enqueue(output)` (guard: `if a.indexingWorker != nil`) (M6, D6)
- [x] 4.5 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test -race ./internal/agent/...` (infra)

## Phase 5 — batch_exec cleanup

- [x] 5.1 `batch.go` line 146: replace `_ = fmt.Errorf("indexing output: %w", indexErr)` with `slog.Warn("batch_exec: failed to index output", "error", indexErr, "command_index", i)` (H1)
- [x] 5.2 `batch.go` lines 116 and 136: replace `fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), i)` with `uuid.New().String()` (remove `time` import if no longer needed) (M7)
- [x] 5.3 `batch.go` lines 154-157: replace byte-slice `preview[:100]` with rune-safe: `runes := []rune(preview); if len(runes) > 100 { preview = string(runes[:100]) + "..." }` (M8)
- [x] 5.4 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/tool/...` (infra)

## Phase 6 — Store hardening

- [x] 6.1 In `internal/store/output.go`, add typed sentinel errors: `ErrOutputMissingID`, `ErrOutputMissingToolName`, `ErrOutputEmptyContent` (M4, D9)
- [x] 6.2 In `sqlitestore.go:IndexOutput`, add validation guard at top: return the typed errors when `output.ID == ""`, `output.ToolName == ""`, `output.Content == ""` (M4, D9)
- [x] 6.3 In `sqlitestore.go`, add `escapeLike(s string) string` helper escaping `\`, `%`, `_`; update `SearchOutputs` LIKE fallback to call `escapeLike(strings.ToLower(query))` and append `ESCAPE '\\'` to each `LIKE` condition (M3, D8)
- [x] 6.4 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/store/...` (infra)

## Phase 7 — Config FileChunkSize removal

- [x] 7.1 Delete `FileChunkSize int` field from `ContextModeConfig` struct in `config.go` (M1, D7)
- [x] 7.2 In `config.go:applyDefaults`, delete both `FileChunkSize` default assignments (auto: line 368, conservative: line 380) and the dead `// Off mode doesn't need` comment referencing it (D7)
- [x] 7.3 In `config.go:validate`, delete `FileChunkSize < 0` check (line 536) (D7)
- [x] 7.4 In `config.go:Load`, switch `yaml.Unmarshal` to strict decoder: `dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded))); dec.KnownFields(true); if err := dec.Decode(&cfg); err != nil { ... }` — this makes configs with `file_chunk_size` field error on load (spec/config override) (D7, M1)
- [x] 7.5 In `internal/config/context_mode_test.go`, remove all `FileChunkSize` assertions (lines 92-94, 146-148, 203-204) and any test fixtures that set the field (D7)
- [x] 7.6 In `internal/agent/loop_test.go` line 1437 and `internal/filter/filter_test.go` line 810, remove `FileChunkSize: 2000` from `ContextModeConfig` literals (D7)
- [x] 7.7 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test ./internal/config/... ./cmd/...` (infra)

## Phase 8 — Test strengthening

- [x] 8.1 In `bounded_exec_test.go`, strengthen "truncates large output" test: assert `result.Metrics.Truncated == true` directly (not via `len > limit && !truncated`); assert `strings.Contains(result.Summary, "[truncated:")` unconditionally; assert head content matches first lines and tail content matches last lines of generated output (T1)
- [x] 8.2 In `bounded_exec_test.go`, remove dead `if err != nil { t.Errorf(...) }` branch from "respects timeout" test (line 193-195 of original) — `Run` never returns non-nil error (T2)
- [x] 8.3 In `bounded_exec_test.go`, tighten timeout bounds to `[50ms, 150ms]` for 100ms timeout (T3)
- [x] 8.4 Add `ErrorKind` table-driven tests in `bounded_exec_test.go`: command-not-found → `ExecErrorStart`; successful cmd → `ExecErrorNone`; non-zero exit (false) → `ExecErrorNone`; timeout → `ExecErrorTimeout`; killed by parent ctx cancel → `ExecErrorKilled` (M5, addendum-B)
- [x] 8.5 In `internal/filter/filter_test.go`, add table-driven test for `PreApply` + `Apply` double-path on a shell command: verify `Meta["microagent/presummarized"]=="true"` after PreApply, verify `Apply` returns unchanged result without applying `applyShell` transforms (T4, M2)
- [x] 8.6 In `internal/store/` (new `output_store_test.go` or extend existing): add tests for `IndexOutput` with empty ID, empty ToolName, empty Content — each must return the corresponding typed error; add LIKE-escape test with query `"50%"` verifying it matches only literal "50%", not everything (T4, M3, M4)
- [x] 8.7 Add porter stemmer test in store tests: index an entry containing "running", search for "run" — must match (T4)
- [x] 8.8 Add pruning decay math test in `internal/store/` verifying that the decay formula's sign is correct (positive decay → score decreases over time, not increases) (T4)
- [x] 8.9 Checkpoint: `go vet ./...` + `golangci-lint run` + `go test -race ./...` (infra)

## Phase 9 — Full CI rehearsal

- [x] 9.1 `go build ./...` — must produce zero errors (rehearsal)
- [x] 9.2 `go vet ./...` — must produce zero warnings (rehearsal)
- [x] 9.3 `go test -race -timeout 300s ./...` — all tests pass with race detector (rehearsal)
- [x] 9.4 `golangci-lint run` — zero lint issues (rehearsal)
