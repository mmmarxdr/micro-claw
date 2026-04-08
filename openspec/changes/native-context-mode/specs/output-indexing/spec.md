# Output Indexing Specification

## Purpose

Indexes tool execution outputs to the existing FTS5 store for later search, avoiding re-execution of commands. Provides a `search_output` tool for querying indexed results.

## Requirements

### Requirement: Automatic Output Indexing

The system SHALL automatically index tool outputs when context mode is `auto` or `conservative`.

#### Scenario: Shell output indexed after execution

- GIVEN context_mode is `auto`
- WHEN shell_exec completes successfully
- THEN the output is indexed to the FTS5 store
- AND metadata includes: tool_name="shell_exec", command, timestamp, truncated flag

#### Scenario: Error output not indexed

- GIVEN context_mode is `auto`
- WHEN shell_exec returns an error
- THEN the output is NOT indexed

#### Scenario: Indexing disabled in off mode

- GIVEN context_mode is `off`
- WHEN any tool executes
- THEN no output is indexed

### Requirement: SearchOutput Tool

The system SHALL provide a `search_output` tool that queries indexed outputs via FTS5.

#### Scenario: Search returns matching outputs

- GIVEN indexed outputs from previous tool executions
- WHEN search_output is called with query "git status"
- THEN results include outputs matching "git status" ranked by relevance
- AND each result includes command, timestamp, and truncated content preview

#### Scenario: Search with no results

- GIVEN indexed outputs
- WHEN search_output is called with query "nonexistent_term_xyz"
- THEN results array is empty

#### Scenario: Search limit respected

- GIVEN 50 indexed outputs
- WHEN search_output is called with limit=5
- THEN at most 5 results are returned

### Requirement: Index Entry Schema

Each indexed output MUST include: id, tool_name, command, content, timestamp, truncated bool, exit_code.

#### Scenario: All fields populated

- GIVEN shell_exec output with truncated=true
- WHEN indexed
- THEN entry has all required fields populated
- AND truncated field is true
- AND exit_code is 0

### Requirement: Store Cleanup

The system SHOULD support configurable TTL-based cleanup of indexed outputs to prevent unbounded growth.

#### Scenario: Old entries cleaned up

- GIVEN indexed outputs older than retention period
- WHEN cleanup runs
- THEN entries older than TTL are removed
