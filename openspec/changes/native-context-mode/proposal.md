# Proposal: Native Context-Mode Token Optimization

## Intent

Build context-mode token optimization natively into the Microclaw binary. External context-mode MCP tools save 60-80% context by intercepting, sandboxing, and indexing tool output. Making this native means every deployment gets optimization by default — single binary, minimal tokens, zero external dependencies.

**Problem**: Current filter system runs post-execution only. Shell outputs 10KB before truncation. File reads enter full context before filtering. No batch execution or output indexing exists.

## Scope

### In Scope (Phase 1)
- Pre-execution heuristic limits for shell (output cap) and file-read (chunk size)
- Sandboxed execution wrapper for shell tool (subprocess isolation, summary-only return)
- Batch execution tool (`batch_exec`) — run N commands, auto-index output, searchable
- Output indexing via existing FTS5 store — search tool results without re-entering context
- Config: `context_mode: auto | conservative | off` with per-tool limits
- No new external dependencies

### Out of Scope
- Sub-agent architecture (Phase 2)
- Security sandboxing with cgroups/network isolation (Phase 3)
- MCP tool sandboxing (Phase 3)
- Filesystem isolation (Phase 3)

## Capabilities

### New Capabilities
- `context-mode-config`: Configuration for pre-execution limits and sandbox behavior
- `sandboxed-execution`: Subprocess wrapper with output capture and summary generation
- `batch-execution`: Execute multiple commands, index output, provide search interface

### Modified Capabilities
- `shell-tool`: Add pre-execution output cap and sandbox integration
- `filter-system`: Add pre-execution hook points (currently post-execution only)
- `config`: Add `ContextMode` struct to `AgentConfig`

## Approach

### Architecture Decisions

1. **Pre-execution limits via tool wrappers** — Shell tool gets `MaxOutputBytes` checked at pipe level (not after). File-read gets configurable `ChunkSize` for streaming reads. Filters get a `PreExecute` hook.

2. **Sandboxed execution** — Wrap `exec.Command` with a `Sandbox` struct: stdout/stderr pipes with byte counters, `context.WithTimeout` for hard kill, summary extraction (first N + last N lines + metrics). Returns `SandboxResult{Summary, Metrics, Truncated bool}`.

3. **Batch execution tool** — New `batch_exec` tool implementing `tool.Tool`. Takes array of commands, runs sequentially, auto-indexes each output to FTS5 store via existing `internal/store`. Returns search-friendly summary. User can `batch_search` to query indexed outputs.

4. **Output indexing** — Reuse existing FTS5 store (`internal/store`). Each tool execution auto-indexes output with metadata (tool, timestamp, truncated flag). Search via new `search_output` tool.

5. **Config structure** — Add to `internal/config/config.go`:
```go
type ContextMode string // "off", "conservative", "auto"

type ContextModeConfig struct {
    Mode              ContextMode
    ShellMaxOutput    int    // bytes, default 4096 (auto), 8192 (conservative)
    FileChunkSize     int    // bytes, default 2000 (auto), 4000 (conservative)
    SandboxTimeout    time.Duration // default 30s
    AutoIndexOutputs  bool   // default true in auto mode
}
```

### Implementation Order
1. Config struct + defaults
2. Pre-execution limits in shell tool + file-read tool
3. Sandbox wrapper
4. Batch execution tool + output indexing
5. Integration tests

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/config/config.go` | Modified | Add `ContextModeConfig` to `AgentConfig` |
| `internal/tool/shell.go` | Modified | Pre-execution output cap, sandbox integration |
| `internal/tool/fileops.go` | Modified | Chunk-based reading |
| `internal/filter/filter.go` | Modified | Add `PreExecute` hook point |
| `internal/tool/batch.go` | New | Batch execution tool |
| `internal/tool/search_output.go` | New | Search indexed outputs |
| `internal/store/` | Modified | Add output indexing metadata |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Pre-execution limits break existing workflows | Medium | Default to `off`, document breaking changes |
| Sandbox overhead impacts performance | Low | Benchmark, make opt-out configurable |
| FTS5 store grows unbounded with auto-indexing | Medium | TTL-based cleanup, configurable index size |
| Batch execution sequential timing issues | Low | Use `context.WithTimeout` per command |

## Rollback Plan

1. Set `context_mode: off` in config — restores pre-change behavior
2. Remove batch-exec and search-output tools from registry
3. Revert filter hook points (additive only, no breaking changes)

## Dependencies

- Existing FTS5 store (`internal/store`) — reuse for output indexing
- Existing filter infrastructure — extend with pre-execution hooks

## Success Criteria

- [ ] Pre-execution limits reduce average tool output context by 50%+
- [ ] Batch execution runs 5+ commands and returns searchable index
- [ ] All existing tests pass with `context_mode: off`
- [ ] Config defaults sensible — `auto` mode requires no user config
- [ ] Zero new external dependencies added
