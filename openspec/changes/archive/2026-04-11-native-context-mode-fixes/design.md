# Design: native-context-mode-fixes

## 1. Context

The `native-context-mode` feature (commits cafea93 → 02b2ef5) shipped with happy-path CI green but
hides 5 HIGH + 9 MEDIUM correctness bugs and 5 weak/missing tests. This change makes it
production-quality without breaking public API or config contracts. Five work groups: sandbox
rework + rename, loop propagation + async indexing, batch cleanup, store hardening, filter cleanup
+ tests. Prior art: proposal `sdd/native-context-mode-fixes/proposal`, review findings id #733.

## 2. Decision Summary

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| D1 | Sandbox rename | **Rename to `BoundedExec`** (type + file `bounded_exec.go`) | Name `Sandbox` is misleading (no FS/net/priv isolation). 11 touch points — all internal. No exported API leaks beyond `tool` package. |
| D2 | combinedBuf rework | **Option A++ (dual head/tail writers, error-swallowing sequentialWriter)** | Option A as originally specified (LimitedWriter + existing sequentialWriter) does NOT work — see §3.2. A++ keeps the rewrite contained and gives exact head+tail guarantees regardless of output size. |
| D3 | ExitCode propagation | **`result.Meta["exit_code"]` (stringly-typed)** | Already used by `preApplyShell` (filter.go:87). No ToolResult struct change needed. Loop parses with strconv.Atoi; fallback 0. |
| D4 | Truncated propagation | **`result.Meta["truncated"]` ("true"/"false")** | Same rationale as D3 — symmetric, no interface change. Loop prefers Meta value over filterMetrics diff. |
| D5 | PreApply coherence marker | **`result.Meta["presummarized"]="true"`** set in `preApplyShell`, checked at top of `filter.Apply` → early return | Avoids double-processing (sandbox Summary mangled by git_diff filters). Zero API change. |
| D6 | Async IndexOutput | **New `IndexingWorker` type in `internal/agent`**, buffered channel (cap 256), single goroutine, drain on Shutdown | Store layer stays pure and synchronous; worker lifetime matches Agent. Buffer 256 sized ~4 KiB per item ≈ 1 MiB worst-case memory. Backpressure: drop-with-slog.Warn. |
| D7 | FileChunkSize field | **Remove field entirely**; `yaml.Unmarshal` non-strict so unknown keys are silently ignored → no config break | Current loader uses `yaml.Unmarshal` (config.go:621) which ignores unknown fields. Users with `file_chunk_size:` in YAML get silent no-op (same as before — it was dead code). Log one-time info on Load if the raw YAML contains the key. |
| D8 | LIKE fallback escape | **`ESCAPE '\\'` clause + escape `%`/`_`/`\\` in query** | Minimal change; keeps fallback useful for queries that have no stemmed keywords. |
| D9 | IndexOutput validation | **Return typed errors** (`ErrMissingID`, `ErrMissingToolName`, `ErrEmptyContent`) from `store` package | Callers can distinguish; regression tests can assert on error values. |

## 3. Architecture — BoundedExec

### 3.1 Rename plan (D1)

Touch points (grep-verified, 11 total):

| File | Lines | Change |
|------|-------|--------|
| `internal/tool/sandbox.go` → `internal/tool/bounded_exec.go` | whole file | `type Sandbox` → `type BoundedExec`; `SandboxMetrics` → `ExecMetrics`; `SandboxResult` → `ExecResult`; update doc comments (see §3.1.1) |
| `internal/tool/batch.go` | 88 | `sandbox := &Sandbox{…}` → `be := &BoundedExec{…}` |
| `internal/filter/filter.go` | 63 | `sb := &tool.Sandbox{…}` → `be := &tool.BoundedExec{…}` |
| `internal/tool/sandbox_test.go` → `internal/tool/bounded_exec_test.go` | 135,162,184,204,235,255,278,316,339,367 | rename + filename |
| `internal/config/config.go` | 65, 368, 380, 536 | Remove `FileChunkSize` + related defaults/validation (D7) |

Keep `SandboxTimeout`, `SandboxKeepFirst`, `SandboxKeepLast` config field names AS-IS — they are
public YAML keys, renaming is a config break. Document the mismatch in a comment.

#### 3.1.1 New doc comment

```go
// BoundedExec wraps exec.Command with two explicit guarantees only:
//
//   1. Output size: stdout+stderr capture is bounded by MaxOutputBytes.
//      Overflow triggers head+tail truncation in the returned Summary.
//   2. Wall-clock timeout: the process is killed when Timeout elapses.
//
// BoundedExec DOES NOT provide process-level sandboxing. It performs NO:
//   - filesystem isolation (the child can read/write anything the parent can)
//   - network isolation
//   - privilege reduction (no seccomp, no user namespace, no chroot)
//   - environment scrubbing (inherits parent env by default)
//
// For true sandboxing use an external tool (bubblewrap, firejail, containers).
type BoundedExec struct { … }
```

### 3.2 combinedBuf rework (D2)

**Why Option A (LimitedWriter wrap) as originally written FAILS**:

`exec.Cmd` spawns an internal goroutine that runs `io.Copy(cmd.Stdout, <pipe>)`. The current
`sequentialWriter.Write` returns `(len(p), ErrMaxBytes)` once countingWriter hits the limit.
`io.Copy` stops on any non-nil writer error. After the first overflow, `io.Copy` aborts, the pipe
is drained/closed, and combinedBuf receives **NOTHING further**. Head is captured (bytes before
overflow) but tail (last N lines of a long-running command) is LOST. Simply wrapping combinedBuf
in a LimitedWriter does not fix this — the wrap doesn't change when the outer write returns an
error to io.Copy.

**Option A++ (chosen)** — three independent buffers, error-free writer:

```
                       ┌─────────────────────────┐
cmd.Stdout ──▶ seqW ──┼─▶ headBuf  (fixed cap)   │ ← first N bytes, stops appending
                       │                         │
                       ├─▶ tailRing (fixed cap)  │ ← ring buffer, keeps last N bytes
                       │                         │
                       └─▶ counter  (no storage) │ ← tracks total bytes + sets truncated flag
                                                 │
cmd.Stderr ──▶ seqW ── (writes to same 3 targets, OR separate head/tail per stream — see §3.2.1)
```

Key property: `seqW.Write` ALWAYS returns `(len(p), nil)`. io.Copy runs to completion. Each inner
writer silently no-ops once full. Head is the first N bytes ever seen. Tail is the last N bytes
ever seen. No data is lost in the gap, it's just not stored — which is the whole point of
"bounded".

#### 3.2.1 Combined vs split streams

Current behavior merges stdout+stderr into `combinedBuf` for Summary extraction. Preserve this:
use ONE triple (headBuf/tailRing/counter) fed by both `cmd.Stdout` and `cmd.Stderr`. Stderr-only
byte count is still tracked via a separate `stderrCounter` for `Metrics.StderrBytes`.

New types in `internal/tool/bounded_exec.go`:

```go
// headBuffer captures up to cap bytes from the start of a stream.
type headBuffer struct { buf []byte; cap int }
func (h *headBuffer) Write(p []byte) (int, error) {
    if len(h.buf) >= h.cap { return len(p), nil } // silent no-op
    n := h.cap - len(h.buf)
    if n > len(p) { n = len(p) }
    h.buf = append(h.buf, p[:n]...)
    return len(p), nil // ALWAYS nil — never stall io.Copy
}

// tailRing keeps the last cap bytes seen.
type tailRing struct { buf []byte; cap int; total int64 }
func (t *tailRing) Write(p []byte) (int, error) {
    t.total += int64(len(p))
    if len(p) >= t.cap {
        t.buf = append(t.buf[:0], p[len(p)-t.cap:]...)
        return len(p), nil
    }
    overflow := len(t.buf) + len(p) - t.cap
    if overflow > 0 { t.buf = t.buf[overflow:] }
    t.buf = append(t.buf, p...)
    return len(p), nil
}

// byteCounter tracks total bytes and sets truncated when over limit.
type byteCounter struct { total int, limit int, truncated bool }
```

`sequentialWriter` is kept but its `Write` becomes:
```go
func (sw *sequentialWriter) Write(p []byte) (int, error) {
    for _, w := range sw.writers { _, _ = w.Write(p) }
    return len(p), nil
}
```

Summary assembly in `BoundedExec.Run`: if `counter.truncated`, build
`string(head.buf) + truncationNotice + string(tail.buf)`. Otherwise (total ≤ MaxOutputBytes),
head and tail overlap — use head alone.

`BoundedExec.Output` string field: **drop**. It was a false contract. Replace with `Head`,
`Tail`, `TotalBytes`, `Truncated` accessors on `ExecResult`. Callers (`batch.go`) that want the
"full" content for indexing get `result.Summary` (truncated head+tail) instead of the old fake
`result.Output`. Document the change as a correction.

## 4. Architecture — Async IndexOutput Worker (D6)

### 4.1 New type

File: `internal/agent/indexing_worker.go`

```go
type IndexingWorker struct {
    store store.OutputStore
    ch    chan store.ToolOutput
    done  chan struct{}
    wg    sync.WaitGroup
}

func NewIndexingWorker(s store.OutputStore) *IndexingWorker {
    return &IndexingWorker{
        store: s,
        ch:    make(chan store.ToolOutput, 256),
        done:  make(chan struct{}),
    }
}

func (w *IndexingWorker) Start(ctx context.Context) {
    w.wg.Add(1)
    go w.run(ctx)
}

func (w *IndexingWorker) run(ctx context.Context) {
    defer w.wg.Done()
    for {
        select {
        case <-ctx.Done():
            // Drain remaining items with a bounded timeout, then exit.
            drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer cancel()
            for {
                select {
                case o := <-w.ch:
                    _ = w.indexOne(drainCtx, o)
                default:
                    close(w.done)
                    return
                }
            }
        case o := <-w.ch:
            _ = w.indexOne(ctx, o)
        }
    }
}

func (w *IndexingWorker) indexOne(ctx context.Context, o store.ToolOutput) error {
    if err := w.store.IndexOutput(ctx, o); err != nil {
        slog.Warn("indexing_worker: failed to index output", "error", err, "id", o.ID)
        return err
    }
    return nil
}

// Enqueue is non-blocking; drops with warning if buffer is full.
func (w *IndexingWorker) Enqueue(o store.ToolOutput) {
    select {
    case w.ch <- o:
    default:
        slog.Warn("indexing_worker: channel full, dropping output", "id", o.ID, "tool", o.ToolName)
    }
}

func (w *IndexingWorker) Stop() { <-w.done }
```

### 4.2 Wiring

- `Agent.indexWorker *IndexingWorker` field added to `internal/agent/agent.go`.
- `New()` constructs it iff `outputStore != nil`.
- `Agent.Run()` calls `a.indexWorker.Start(ctx)` alongside other background workers.
- `Agent.Shutdown()` calls `a.indexWorker.Stop()` AFTER channel.Stop() (so no new events arrive
  during drain). The existing run loop already cancels ctx on `ctx.Done()`, which triggers drain.
- `loop.go:249` replaced: instead of `a.outputStore.IndexOutput(ctx, output)` call
  `a.indexWorker.Enqueue(output)`.

### 4.3 Failure modes

| Mode | Behavior |
|------|----------|
| Channel full (burst) | Drop with `slog.Warn`; agent keeps running. Test: fill channel with stub slow store, assert drops logged. |
| Store error on one item | Logged via `indexOne`; worker continues with next item. Test: stub store returning error on Nth call. |
| Shutdown mid-enqueue | `Enqueue` after Stop is a send on a non-closed channel that no receiver reads → falls to `default` branch → drops. Safe. Test: call Enqueue after Stop, assert no panic and warn logged. |

### 4.4 ASCII happy path

```
loop.go ──Enqueue──▶ chan(256) ──worker.run──▶ store.IndexOutput
   │                                                │
   │                                                └─▶ slog.Warn on error
   │
   └─ ctx.Done() ──▶ Shutdown ──▶ drain loop (2s cap) ──▶ close(done)
```

## 5. Architecture — ToolResult extensions (D3, D4, D5)

**No struct changes**. `ToolResult.Meta map[string]string` carries all new signals.

### 5.1 Contract additions

| Key | Set by | Read by | Values |
|-----|--------|---------|--------|
| `exit_code` | `preApplyShell` (already) + `batch.go` via BoundedExec | `loop.go` IndexOutput path | decimal string, e.g. `"0"`, `"127"`, `"-1"` |
| `truncated` | `preApplyShell` (NEW), `batch.go` (NEW) | `loop.go` IndexOutput path | `"true"` / `"false"` |
| `presummarized` | `preApplyShell` (NEW) | `filter.Apply` (NEW early-return) | `"true"` iff set |

### 5.2 Loop.go changes (lines 225-252)

```go
// Prefer meta propagated by PreApply; fall back to filter-derived values.
exitCode := 0
if v := result.Meta["exit_code"]; v != "" {
    if parsed, err := strconv.Atoi(v); err == nil { exitCode = parsed }
}
truncated := filterMetrics.CompressedBytes < filterMetrics.OriginalBytes
if v := result.Meta["truncated"]; v != "" {
    truncated = v == "true"
}
output := store.ToolOutput{ … ExitCode: exitCode, Truncated: truncated … }
a.indexWorker.Enqueue(output)
```

### 5.3 Filter.Apply early return (D5)

```go
func Apply(toolName string, input json.RawMessage, result tool.ToolResult, cfg config.FilterConfig) (tool.ToolResult, Metrics) {
    if !cfg.Enabled || result.IsError { return result, Metrics{} }
    if result.Meta["presummarized"] == "true" { return result, Metrics{} } // D5
    …
}
```

## 6. Architecture — Config migration (D7)

### 6.1 Removal

Delete from `internal/config/config.go`:
- Line 65: `FileChunkSize int` field
- Lines 368-370 and 380-382: default assignment branches
- Lines 536-538: negative validation branch

### 6.2 YAML compatibility

`yaml.Unmarshal([]byte, &cfg)` (config.go:621) is **non-strict** — gopkg.in/yaml.v3's default
`Unmarshal` ignores unknown fields. Existing configs with `file_chunk_size: N` continue to Load
without error. The value becomes a silent no-op (which is what it was anyway — dead code).

**No migration warning required** — the field was never functional. Tests in
`internal/config/context_mode_test.go:92,146,178-204` must be deleted/updated to stop asserting
on `FileChunkSize`. `internal/agent/loop_test.go:1437` and `internal/filter/filter_test.go:810`
also need the field removed from test fixtures.

If we wanted stricter behavior (out of scope): switch to
`yaml.NewDecoder(bytes.NewReader(expanded)).KnownFields(true).Decode(&cfg)` — but this would break
ANY YAML with unknown keys, a much bigger behavioral change. Skip.

### 6.3 preApplyFileRead removal

Delete `preApplyFileRead` (filter.go:107-129) and the `case "read_file"` branch in `PreApply`
(filter.go:40-41). Delete `FileChunkSize` references in all tests.

## 7. Architecture — Store hardening (D8, D9)

### 7.1 LIKE escape (sqlitestore.go:532, D8)

```go
// Escape LIKE metacharacters so user input is matched literally.
func escapeLike(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `%`, `\%`)
    s = strings.ReplaceAll(s, `_`, `\_`)
    return s
}
// …
likePattern := "%" + escapeLike(strings.ToLower(query)) + "%"
q := `SELECT ` + cols + `
      FROM tool_outputs
      WHERE lower(content) LIKE ? ESCAPE '\' OR lower(tool_name) LIKE ? ESCAPE '\' OR lower(command) LIKE ? ESCAPE '\'
      ORDER BY timestamp DESC`
```

### 7.2 IndexOutput validation (sqlitestore.go:490, D9)

New errors in `internal/store/output.go`:
```go
var (
    ErrOutputMissingID       = errors.New("ToolOutput.ID is required")
    ErrOutputMissingToolName = errors.New("ToolOutput.ToolName is required")
    ErrOutputEmptyContent    = errors.New("ToolOutput.Content is required")
)
```

At top of `IndexOutput`:
```go
if output.ID == "" { return ErrOutputMissingID }
if output.ToolName == "" { return ErrOutputMissingToolName }
if output.Content == "" { return ErrOutputEmptyContent }
```

## 8. Test Strategy

Table-driven where it fits. All new tests under existing files or new `*_test.go` siblings.

| Finding | Test type | Location | What it asserts |
|---------|-----------|----------|-----------------|
| H1 | Unit | `internal/tool/batch_test.go` (NEW) | slog hook captures warning when stub store returns error |
| H2 | Unit | `internal/agent/loop_test.go` | shell_exec with `Meta[exit_code]="127"` → indexed `ExitCode=127` |
| H3 | Unit | `internal/agent/loop_test.go` | shell_exec with `Meta[truncated]="true"` → indexed `Truncated=true`, regardless of filterMetrics |
| H4 | Unit | `internal/tool/bounded_exec_test.go` | Run a command emitting 10KB with MaxOutputBytes=1KB, KeepFirstN=5, KeepLastN=5; assert summary contains BOTH the first 5 lines AND the last 5 lines AND the truncation notice. Currently fails. |
| H5 | Unit | same | Run command emitting 10MB; assert total memory held by head+tail ≤ 2×cap, no OOM |
| M1 | Unit | `internal/filter/filter_test.go` | `PreApply("read_file", …)` path removed → no-op behavior; verify `FileChunkSize` no longer referenced |
| M2 | Unit | `internal/filter/filter_test.go` (NEW test) | Apply with `Meta[presummarized]="true"` returns result unchanged, Metrics zero |
| M3 | Unit | `internal/store/sqlitestore_test.go` | Insert content `"50% off"` and `"50 off"`; query `"50%"` matches only the first. Currently matches both. |
| M4 | Unit | `internal/store/sqlitestore_test.go` | IndexOutput with empty ID/ToolName/Content returns the respective typed error |
| M5 | Unit | `internal/tool/bounded_exec_test.go` | Start error: command `"nonexistent-xyz"` → ExecResult.ErrorKind=="start_err"; timeout: `"sleep 10"` with Timeout=50ms → ErrorKind=="timeout" |
| M6 | Integration | `internal/agent/indexing_worker_test.go` (NEW) | Enqueue N items, worker drains via Stop; verify all stored. Drop path: fill channel with slow stub, verify warns logged + no block |
| M7 | Unit | `internal/tool/batch_test.go` | Run two commands; assert IDs are uuid format and unique |
| M8 | Unit | `internal/tool/batch_test.go` | Preview of output containing multi-byte UTF-8 at boundary 100 — assert `utf8.ValidString(preview)` |
| M9 | — | — | Covered by D1 rename — the doc comment IS the fix |
| T1 | Strengthen | `internal/tool/bounded_exec_test.go:225` | Replace tautological assertion: assert `Metrics.Truncated == true` AND `strings.Contains(Summary, "[truncated:")` AND summary starts with first line content AND ends with last line content |
| T2 | Fix | same, line 193 | Remove dead branch; Run never returns non-nil per current signature. Optionally, add ErrorKind assertion from M5 |
| T3 | Strengthen | same, line 198 | Tighten timeout test: assert duration ≤ Timeout+50ms AND ExitCode == -1 AND ErrorKind == "timeout" |
| T4a | NEW | `internal/filter/filter_test.go` | PreApply+Apply end-to-end: stub shell tool, verify presummarized prevents git_diff mangling |
| T4b | NEW | `internal/store/sqlitestore_test.go` | LIKE fallback covered by M3 |
| T4c | NEW | `internal/store/sqlitestore_test.go` | Covered by M4 |

Run matrix: `go vet ./...`, `golangci-lint run`, `go test -race ./...` before commit.

## 9. Migration & Compatibility

- **FileChunkSize removal**: silent compat thanks to `yaml.Unmarshal` non-strict mode. Old YAMLs
  continue to parse. Programmatic callers (tests only, grep-verified) updated in-tree.
- **BoundedExec rename**: `tool.Sandbox` is package-internal API only. External users (none found)
  would break. Proposal explicitly accepts this as the correctness price.
- **Meta keys**: additive. Any third-party tool not setting them gets old behavior.
- **Store errors**: new typed errors. Existing callers that only check `err != nil` keep working.
- **LIKE escape**: stricter matching — previously `50%` matched everything, now only literal.
  Behavioral change but strictly an improvement; no public API surface affected.

## 10. Risks & Rollback

| # | Risk | Mitigation | Rollback |
|---|------|------------|----------|
| R1 | BoundedExec rework breaks existing truncation tests in subtle ways | Comprehensive bounded_exec_test.go with table-driven cases covering: no truncation, exact-limit, head-only overflow, tail-only overflow, mixed stdout+stderr, empty output. Run `-race`. | Revert file; rename alone can ship standalone if combinedBuf fix fails. |
| R2 | Async worker drops outputs under load | Buffer sized 256 (generous for human-driven agents); drops logged; test exercises drop path. Future: expose metric. | Synchronous fallback via feature flag env var `MICROAGENT_SYNC_INDEX=1` (if needed — not shipped by default). |
| R3 | Removing FileChunkSize breaks a downstream that reads the struct field programmatically | Grep shows only internal test usage. Field is `int` with default 0 — any downstream reading it never got real data anyway. | Re-add as deprecated no-op field with `// Deprecated: reserved, no effect` — costs one line. |

## 11. Out of Scope

- Phase 2 file chunking (including real `preApplyFileRead`)
- Real process sandboxing (namespaces, cgroups, seccomp)
- Switching yaml.v3 to strict `KnownFields(true)` mode
- Phase 7 provider catalog changes
- Any public config YAML key rename (`sandbox_*` keys stay as-is)
- Exposing indexing worker metrics to audit
