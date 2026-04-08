# Technical Design: Native Context-Mode

## Overview

Native context-mode brings token optimization directly into the Microclaw binary. It adds pre-execution output limits, sandboxed shell execution, batch execution with FTS5 indexing, and a search tool for indexed outputs — all without external dependencies.

## Architecture Decisions

### AD1: Pre-Execution Limits via Tool Wrappers

**Problem**: Current filter system is post-execution only. Shell outputs up to 10KB into context before truncation. File reads enter full content before filtering.

**Solution**: Add `PreExecute` hook to the filter pipeline. Shell tool gets `MaxOutputBytes` enforced at the `io.Pipe` level (writer side). File-read tool gets configurable `ChunkSize` for streaming reads.

**Integration Point**: The agent loop at `internal/agent/loop.go:196-205` currently calls `executeWithRecover()` then `filter.Apply()`. PreExecute runs BEFORE `executeWithRecover()` — it can short-circuit execution by returning a capped `ToolResult` directly.

```go
// internal/filter/filter.go — new hook
type PreExecuteFunc func(input json.RawMessage, cfg config.ContextModeConfig) (tool.ToolResult, bool)

// PreApply runs before tool execution. Returns (result, true) to skip execution.
func PreApply(toolName string, input json.RawMessage, cfg config.ContextModeConfig) (tool.ToolResult, bool)
```

**Shell PreExecute**: Extract command from JSON input. If context-mode is enabled, return a synthetic execution that pipes through a byte-limited writer. The actual execution happens inside the sandbox (AD2).

**File PreExecute**: Parse `path` and `offset`/`limit`. Apply `ChunkSize` cap to limit bytes read.

### AD2: Sandbox Wrapper on exec.Command

**Problem**: Shell tool calls `exec.CommandContext()` directly with no byte accounting. Output can be arbitrarily large.

**Solution**: `Sandbox` struct wraps `exec.Command` with byte-counting pipes and timeout enforcement.

```go
// internal/tool/sandbox.go — new file
type SandboxMetrics struct {
    StdoutBytes int
    StderrBytes int
    ExitCode    int
    Duration    time.Duration
    Truncated   bool
}

type SandboxResult struct {
    Summary  string         // first N lines + last N lines + metrics footer
    Output   string         // full output (used by batch_exec indexing)
    Metrics  SandboxMetrics
}

type Sandbox struct {
    MaxOutputBytes int
    Timeout        time.Duration
    KeepFirstN     int   // lines to keep from head (default 20)
    KeepLastN      int   // lines to keep from tail (default 10)
}

func (s *Sandbox) Run(ctx context.Context, name string, args ...string) (SandboxResult, error)
```

**Pipe Management**:
1. `cmd.StdoutPipe()` → wrap in `countingWriter` (increments `bytesWritten`, returns `ErrMaxBytes` when limit hit)
2. `cmd.StderrPipe()` → same counting writer
3. Both writers feed into a shared `bytes.Buffer` for the full output (used by batch indexing)
4. When either writer hits `MaxOutputBytes`, set `Truncated = true`, kill the process via `cancel()`
5. Build summary: `firstN lines + "\n... [truncated, X/Y bytes]\n...\n" + lastN lines + metrics footer`

**Timeout**: `context.WithTimeout(ctx, s.Timeout)` — hard kill at deadline. Default 30s.

**Summary Extraction Algorithm**:
```
if truncated:
  lines = split(output, "\n")
  head = lines[0:min(KeepFirstN, len(lines))]
  tail = lines[max(0, len(lines)-KeepLastN):]
  summary = join(head) + "\n...\n[truncated: {outputBytes}/{maxBytes} bytes, {duration}ms]\n...\n" + join(tail)
else:
  summary = output  // no truncation, return full
```

### AD3: Batch Execution Tool

**Problem**: No way to run multiple commands without flooding context. Each shell_exec returns its full (filtered) output as a separate message.

**Solution**: New `batch_exec` tool implementing `tool.Tool`. Runs N commands sequentially, indexes each output to FTS5, returns a compact summary.

```go
// internal/tool/batch.go — new file
type BatchExecTool struct {
    sandbox   Sandbox
    store     OutputStore  // subset of store.Store for output indexing
}

type BatchExecInput struct {
    Commands []string `json:"commands"` // array of shell commands
}

type BatchExecOutput struct {
    Results []BatchResultSummary `json:"results"`
    TotalDuration  time.Duration  `json:"total_duration"`
}

type BatchResultSummary struct {
    Command   string `json:"command"`
    ExitCode  int    `json:"exit_code"`
    OutputLen int    `json:"output_len"`
    Truncated bool   `json:"truncated"`
    IndexID   string `json:"index_id,omitempty"` // FTS5 doc ID for search
}
```

**Execution Flow**:
1. Parse `BatchExecInput.Commands` from JSON params
2. For each command:
   - Run via `Sandbox.Run()`
   - Index full output to FTS5 store with metadata (command, timestamp, exit_code, truncated)
   - Append `BatchResultSummary` to results
3. Return compact JSON summary (NOT the actual outputs — those are searchable via `search_output`)

### AD4: Output Indexing

**Problem**: Tool outputs are lost after entering context. No way to reference past results without re-executing.

**Solution**: Reuse existing FTS5 store (`internal/store`). Add a new `OutputStore` interface for output-specific operations.

```go
// internal/store/output.go — new file
type ToolOutput struct {
    ID        string    `json:"id"`
    ToolName  string    `json:"tool_name"`
    Command   string    `json:"command,omitempty"`
    Content   string    `json:"content"`
    Truncated bool      `json:"truncated"`
    ExitCode  int       `json:"exit_code"`
    Timestamp time.Time `json:"timestamp"`
}

type OutputStore interface {
    IndexOutput(ctx context.Context, output ToolOutput) error
    SearchOutputs(ctx context.Context, query string, limit int) ([]ToolOutput, error)
}
```

**Implementation**: `SQLiteStore` gets a new `tool_outputs` FTS5 table:
```sql
CREATE VIRTUAL TABLE IF NOT EXISTS tool_outputs USING fts5(
    id UNINDEXED,
    tool_name,
    command,
    content,
    truncated UNINDEXED,
    exit_code UNINDEXED,
    timestamp UNINDEXED,
    tokenize='porter'
);
```

`FileStore` gets a no-op implementation (returns `nil`) — FTS5 indexing only works with SQLite.

**Integration**: After each tool execution in the agent loop, if `ContextModeConfig.AutoIndexOutputs` is true, call `store.IndexOutput()`. The `batch_exec` tool also calls this for each command.

### AD5: Configuration

**Problem**: No config structure for context-mode behavior.

**Solution**: Add `ContextModeConfig` to `AgentConfig`.

```go
// internal/config/config.go — additions

type ContextMode string

const (
    ContextModeOff          ContextMode = "off"
    ContextModeConservative ContextMode = "conservative"
    ContextModeAuto         ContextMode = "auto"
)

type ContextModeConfig struct {
    Mode              ContextMode     `yaml:"mode"`               // default: "off"
    ShellMaxOutput    int             `yaml:"shell_max_output"`   // bytes, default 4096 (auto), 8192 (conservative)
    FileChunkSize     int             `yaml:"file_chunk_size"`    // bytes, default 2000 (auto), 4000 (conservative)
    SandboxTimeout    time.Duration   `yaml:"sandbox_timeout"`    // default 30s
    AutoIndexOutputs  bool            `yaml:"auto_index_outputs"` // default true in auto mode
    SandboxKeepFirst  int             `yaml:"sandbox_keep_first"` // default 20 lines
    SandboxKeepLast   int             `yaml:"sandbox_keep_last"`  // default 10 lines
}
```

Added to `AgentConfig`:
```go
type AgentConfig struct {
    // ... existing fields ...
    ContextMode ContextModeConfig `yaml:"context_mode"`
}
```

Defaults applied in `applyDefaults()`:
- `Mode`: `"off"` (opt-in, no behavior change)
- `ShellMaxOutput`: 4096 (auto), 8192 (conservative)
- `FileChunkSize`: 2000 (auto), 4000 (conservative)
- `SandboxTimeout`: 30s
- `AutoIndexOutputs`: true (auto), false (conservative), false (off)
- `SandboxKeepFirst`: 20
- `SandboxKeepLast`: 10

## Data Flow

### Standard Tool Execution (with context-mode enabled)

```
LLM Tool Call
  │
  ▼
Agent Loop (loop.go:196)
  │
  ├─ PreApply(toolName, input, ctxModeCfg)  [NEW]
  │   ├─ Returns (result, true) → SKIP execution, use result
  │   └─ Returns (_, false)  → CONTINUE to execution
  │
  ▼
validateToolInput(input, schema)            [EXISTING]
  │
  ▼
executeWithRecover(ctx, tool, input)        [EXISTING]
  │  └─ tool.Execute() runs
  │      └─ shell_exec: Sandbox.Run()       [NEW - replaces exec.Command]
  │          ├─ countingWriter pipes
  │          ├─ timeout enforcement
  │          └─ summary extraction
  │
  ▼
filter.Apply(toolName, input, result, cfg)  [EXISTING - unchanged]
  │
  ▼
Auto-Index (if enabled)                     [NEW]
  │  └─ store.IndexOutput(toolOutput)
  │
  ▼
Audit Event + Message Append                [EXISTING - unchanged]
```

### Batch Exec Flow

```
LLM calls batch_exec
  │
  ▼
BatchExecTool.Execute(input)
  │
  ├─ For each command:
  │   ├─ Sandbox.Run(command)
  │   ├─ store.IndexOutput(result)   → FTS5
  │   └─ append BatchResultSummary
  │
  ▼
Return compact JSON (summaries only, outputs searchable)
  │
  ▼
Agent Loop (same path as standard tool)
```

### Search Output Flow

```
LLM calls search_output
  │
  ▼
SearchOutputTool.Execute(query)
  │
  ├─ store.SearchOutputs(query, limit)  → FTS5 MATCH
  │
  ▼
Return matching ToolOutput entries
```

## New Files

| File | Purpose |
|------|---------|
| `internal/tool/sandbox.go` | Sandbox struct, countingWriter, Run(), summary extraction |
| `internal/tool/batch.go` | BatchExecTool implementing tool.Tool |
| `internal/tool/search_output.go` | SearchOutputTool implementing tool.Tool |
| `internal/store/output.go` | OutputStore interface, ToolOutput struct |
| `internal/tool/sandbox_test.go` | Sandbox unit tests |
| `internal/tool/batch_test.go` | BatchExec tests |
| `internal/tool/search_output_test.go` | SearchOutput tests |

## Modified Files

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `ContextModeConfig` struct, add to `AgentConfig`, add defaults |
| `internal/tool/shell.go` | Replace `exec.Command` with `Sandbox.Run()`, use `ContextModeConfig` |
| `internal/tool/fileops.go` | Add chunk-size limiting in `ReadFileTool.Execute()` |
| `internal/filter/filter.go` | Add `PreApply()` function |
| `internal/tool/registry.go` | Register `BatchExecTool` and `SearchOutputTool` when context-mode enabled |
| `internal/agent/loop.go` | Call `PreApply()` before `executeWithRecover()`, call `IndexOutput()` after |
| `internal/store/sqlitestore.go` | Add `tool_outputs` FTS5 table, implement `OutputStore` |
| `internal/store/filestore.go` | Add no-op `OutputStore` implementation |

## Key Design Constraints

1. **Context Mode is OFF by default** — zero behavior change for existing deployments
2. **No new external dependencies** — uses existing SQLite FTS5 and stdlib `exec.Command`
3. **FileStore gets no-op OutputStore** — FTS5 indexing only meaningful with SQLite
4. **Sandbox is for shell only (Phase 1)** — MCP tools and HTTP are not sandboxed yet
5. **PreApply is opt-in** — only runs when `Mode != "off"`
6. **Full output preserved for indexing** — sandbox captures full output for FTS5, only the LLM-facing result gets truncated/summarized
