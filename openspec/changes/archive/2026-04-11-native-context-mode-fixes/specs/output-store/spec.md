# Delta for Output Store (output-store)

## Overview

Fixes two bugs in `sqlitestore.go`: `SearchOutputs` LIKE fallback does not escape `%` and `_` metacharacters (M3), and `IndexOutput` accepts empty required fields without returning an error (M4).

## MODIFIED Requirements

### Requirement: SearchOutput Tool

The system SHALL provide a `search_output` tool that queries indexed outputs via FTS5.

(Previously: LIKE fallback path used unescaped user input — a query containing `%` or `_` would match more rows than intended. No SQL injection risk (params bound), but semantically incorrect results.)

#### Scenario: Search returns matching outputs (existing — unchanged)

- GIVEN indexed outputs from previous tool executions
- WHEN search_output is called with query "git status"
- THEN results include outputs matching "git status" ranked by relevance
- AND each result includes command, timestamp, and truncated content preview

#### Scenario: Search with no results (existing — unchanged)

- GIVEN indexed outputs
- WHEN search_output is called with query "nonexistent_term_xyz"
- THEN results array is empty

#### Scenario: Search limit respected (existing — unchanged)

- GIVEN 50 indexed outputs
- WHEN search_output is called with limit=5
- THEN at most 5 results are returned

#### Scenario: LIKE query containing percent literal

- GIVEN indexed output with content "CPU at 50% usage"
- AND the FTS query builder returns empty (no keywords in "50%")
- WHEN search_output is called with query "50%"
- THEN only the entry containing "50%" is returned
- AND the query does NOT match all rows (percent is treated as literal)

#### Scenario: LIKE query containing underscore literal

- GIVEN indexed outputs with commands "run_test" and "runXtest"
- AND the FTS query builder returns empty
- WHEN search_output is called with query "run_test"
- THEN only "run_test" matches
- AND "runXtest" does NOT match

### Requirement: Index Entry Schema

Each indexed output MUST include: id, tool_name, command, content, timestamp, truncated bool, exit_code. Fields `id`, `tool_name`, and `content` are REQUIRED — `IndexOutput` MUST return an error if any of them is empty.

(Previously: `IndexOutput` accepted empty values, inserting rows that violate the implicit contract; callers had no way to detect the programming error.)

#### Scenario: All fields populated (existing — unchanged)

- GIVEN shell_exec output with truncated=true
- WHEN indexed
- THEN entry has all required fields populated
- AND truncated field is true
- AND exit_code reflects actual process exit code

#### Scenario: IndexOutput rejects empty ID

- GIVEN a `ToolOutput` with `ID = ""`
- WHEN `IndexOutput` is called
- THEN it returns a non-nil error containing "ID"
- AND no row is inserted into the store

#### Scenario: IndexOutput rejects empty ToolName

- GIVEN a `ToolOutput` with `ToolName = ""`
- WHEN `IndexOutput` is called
- THEN it returns a non-nil error containing "ToolName"
- AND no row is inserted into the store

#### Scenario: IndexOutput rejects empty Content

- GIVEN a `ToolOutput` with `Content = ""`
- WHEN `IndexOutput` is called
- THEN it returns a non-nil error containing "Content"
- AND no row is inserted into the store

#### Scenario: IndexOutput succeeds with all required fields set

- GIVEN a `ToolOutput` with valid ID, ToolName, and Content
- WHEN `IndexOutput` is called
- THEN it returns nil
- AND the row is retrievable via `SearchOutputs`

### Requirement: Store Cleanup

The system SHOULD support configurable TTL-based cleanup of indexed outputs to prevent unbounded growth.

(Previously: unchanged — carried forward intact)

#### Scenario: Old entries cleaned up

- GIVEN indexed outputs older than retention period
- WHEN cleanup runs
- THEN entries older than TTL are removed
