# Batch Execution Specification

## Purpose

Provides a `batch_exec` tool that runs multiple commands sequentially, auto-indexes each output to FTS5 store, and returns a searchable summary. Complements individual tool execution with multi-command workflows.

## Requirements

### Requirement: BatchExec Tool Registration

The system SHALL register a `batch_exec` tool implementing tool.Tool when context mode is not `off`.

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

#### Scenario: Commands run in order

- GIVEN batch_exec with commands ["echo first", "echo second", "echo third"]
- WHEN executed
- THEN commands run sequentially
- AND each result includes the command index (0, 1, 2)

#### Scenario: Failure stops execution

- GIVEN batch_exec with commands ["echo ok", "exit 1", "echo never"]
- AND stop_on_error is true
- WHEN executed
- THEN first command succeeds
- AND second command fails
- AND third command is NOT executed

#### Scenario: Continue on error when configured

- GIVEN batch_exec with commands ["echo ok", "exit 1", "echo also ok"]
- AND stop_on_error is false
- WHEN executed
- THEN all three commands execute
- AND results array has 3 entries with individual success/failure flags

### Requirement: Output Auto-Indexing

Each command's output SHALL be auto-indexed to the FTS5 store with metadata (tool name, timestamp, command, truncated flag).

#### Scenario: Outputs indexed automatically

- GIVEN batch_exec runs 3 commands
- WHEN all complete
- THEN each output is indexed to the FTS5 store
- AND indexed entries include command string and timestamp

#### Scenario: Indexing skipped when auto-index disabled

- GIVEN auto_index_outputs is false
- WHEN batch_exec completes
- THEN outputs are NOT indexed to the FTS5 store

### Requirement: Batch Result Format

The system SHALL return a structured BatchResult containing per-command results and a summary.

#### Scenario: Result includes all fields

- GIVEN batch_exec with 2 commands
- WHEN executed
- THEN BatchResult has Results array with 2 entries
- AND each entry has: Command, Output, ExitCode, Duration, Truncated, IsError
- AND BatchResult has Summary with pass/fail counts
