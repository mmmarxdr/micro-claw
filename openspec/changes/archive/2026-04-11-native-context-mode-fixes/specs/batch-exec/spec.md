# Delta for Batch Execution (batch-exec)

## Overview

Fixes three bugs in `batch.go`: swallowed indexing errors on the success path (H1), non-unique IDs from `time.Now().UnixNano()` (M7), and byte-boundary slicing of UTF-8 preview strings (M8).

## MODIFIED Requirements

### Requirement: BatchExec Tool Registration

The system SHALL register a `batch_exec` tool implementing tool.Tool when context mode is not `off`.

(Previously: unchanged — carried forward intact)

#### Scenario: Tool available in registry

- GIVEN context_mode is `auto`
- WHEN the tool registry is initialized
- THEN `batch_exec` is available in the tool list

#### Scenario: Tool absent when disabled

- GIVEN context_mode is `off`
- WHEN the tool registry is initialized
- THEN `batch_exec` is not in the tool list

### Requirement: Sequential Command Execution

The system MUST execute commands in the provided array sequentially, in order.

(Previously: unchanged — carried forward intact)

#### Scenario: Commands run in order

- GIVEN batch_exec with commands `["echo first", "echo second", "echo third"]`
- WHEN executed
- THEN commands run sequentially in order 0, 1, 2

#### Scenario: Failure stops execution

- GIVEN batch_exec with commands `["echo ok", "exit 1", "echo never"]` and `stop_on_error=true`
- WHEN executed
- THEN third command is NOT executed

#### Scenario: Continue on error when configured

- GIVEN batch_exec with commands `["echo ok", "exit 1", "echo also ok"]` and `stop_on_error=false`
- WHEN executed
- THEN all three commands execute and results array has 3 entries

### Requirement: Output Auto-Indexing

Each command's output SHALL be auto-indexed to the FTS5 store with metadata. Indexing failures on the success path MUST be logged via `slog.Warn` and MUST NOT be silently discarded.

(Previously: indexing failure on the success path used `_ = fmt.Errorf(...)` — the error was created and immediately discarded. Only the error path correctly called `slog.Warn`.)

#### Scenario: Indexing error on success path is logged

- GIVEN batch_exec runs a command that succeeds
- AND the OutputStore returns an error from IndexOutput
- WHEN the success path executes
- THEN a `slog.Warn` is emitted with `"error"` and `"command_index"` fields
- AND the batch execution continues (not aborted)

#### Scenario: Regression — silent discard no longer compiles

- GIVEN the batch.go success-path indexing block
- WHEN reviewed
- THEN there is NO `_ = fmt.Errorf(...)` call in that block

#### Scenario: Indexing skipped when auto-index disabled

- GIVEN auto_index_outputs is false
- WHEN batch_exec completes
- THEN outputs are NOT indexed to the FTS5 store

### Requirement: Unique Output IDs

Each indexed `ToolOutput.ID` MUST be globally unique. IDs MUST be generated using `uuid.New().String()`.

(Previously: IDs used `fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), i)` — UnixNano is not strictly monotonic on all platforms; concurrent or rapid successive calls can collide. FTS5 has no PRIMARY KEY enforcement, so collisions are silently inserted.)

#### Scenario: IDs are UUID v4

- GIVEN batch_exec runs 3 commands
- WHEN each output is indexed
- THEN each ToolOutput.ID matches the UUID v4 format (`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)

#### Scenario: IDs are distinct across commands in the same batch

- GIVEN batch_exec runs 5 commands simultaneously
- WHEN outputs are indexed
- THEN all 5 IDs are distinct strings

### Requirement: Batch Result Format

The system SHALL return a structured result containing per-command results and a summary.

(Previously: unchanged — carried forward intact; preview slicing bug fixed under separate requirement)

#### Scenario: Result includes all fields

- GIVEN batch_exec with 2 commands
- WHEN executed
- THEN result `Content` has pass/fail counts for both commands

### Requirement: UTF-8 Safe Preview

The preview string in each summary line MUST be truncated at a Unicode rune boundary, not a byte boundary.

(Previously: `preview[:100]` sliced by bytes — a multi-byte codepoint straddling byte 100 produces invalid UTF-8 and corrupt output.)

#### Scenario: Multi-byte codepoint not split

- GIVEN a command whose output preview contains a 3-byte UTF-8 character (e.g. `\u4e2d`) at positions 99–101
- WHEN the preview is truncated to 100 characters
- THEN the resulting preview string is valid UTF-8
- AND the character is either fully included or fully excluded (not split)

#### Scenario: ASCII-only preview unchanged

- GIVEN a command output with a 150-byte ASCII preview
- WHEN truncated
- THEN the result is exactly 100 bytes followed by `"..."`
