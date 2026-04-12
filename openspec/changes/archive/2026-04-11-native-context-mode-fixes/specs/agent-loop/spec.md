# Delta for Agent Loop (agent-loop)

## Overview

Fixes two correctness bugs in `loop.go` auto-indexing: exit code hardcoded to 0 (H2) and `Truncated` derived from filter metrics instead of sandbox metrics (H3). Also fixes the double-processing coherence issue (M2) where `filter.Apply` could mangle a pre-summarized sandbox result.

## MODIFIED Requirements

### Requirement: Automatic Output Indexing

The system SHALL automatically index tool outputs when context mode is `auto` or `conservative`. The indexed `ExitCode` MUST reflect the actual process exit code. The indexed `Truncated` flag MUST reflect whether the producing tool (sandbox or filter) truncated the output.

(Previously: `ExitCode` was hardcoded to 0 with comment "No way to get actual exit code without changing tool interface". `Truncated` was `filterMetrics.CompressedBytes < filterMetrics.OriginalBytes` — for pre-apply intercepted shell_exec the filter metrics are zero, so Truncated was always false even when sandbox did truncate.)

#### Scenario: Actual exit code stored for pre-apply intercepted shell_exec

- GIVEN context_mode is `auto`
- AND shell_exec runs `sh -c "exit 127"`
- WHEN the result is auto-indexed
- THEN `ToolOutput.ExitCode` equals 127
- AND the index entry does NOT contain 0

#### Scenario: ExitCode 0 stored for successful command

- GIVEN context_mode is `auto`
- AND shell_exec runs `echo ok`
- WHEN the result is auto-indexed
- THEN `ToolOutput.ExitCode` equals 0

#### Scenario: Truncated true when sandbox truncated output

- GIVEN context_mode is `auto`
- AND shell_exec runs a command producing output exceeding `ShellMaxOutput`
- WHEN the result is auto-indexed
- THEN `ToolOutput.Truncated` is `true`

#### Scenario: Truncated false when output fits within limits

- GIVEN context_mode is `auto`
- AND shell_exec runs `echo small`
- WHEN the result is auto-indexed
- THEN `ToolOutput.Truncated` is `false`

#### Scenario: Shell output indexed after execution (existing — unchanged)

- GIVEN context_mode is `auto`
- WHEN shell_exec completes successfully
- THEN the output is indexed to the FTS5 store
- AND metadata includes: tool_name="shell_exec", command, timestamp

#### Scenario: Error output not indexed (existing — unchanged)

- GIVEN context_mode is `auto`
- WHEN shell_exec returns `IsError=true`
- THEN the output is NOT indexed

### Requirement: PreApply + Apply Coherence

When `preApplyShell` has already summarized the output (setting `result.Meta["truncated"]`), `filter.Apply` MUST NOT re-process the content as if it were raw shell output.

(Previously: `filter.Apply` ran unconditionally after pre-apply, causing `applyShell` to apply git_diff/git_log pattern filters to the already-summarized sandbox content — potentially mangling it.)

#### Scenario: Apply skips re-processing of pre-summarized results

- GIVEN context_mode is `auto`
- AND shell_exec pre-apply returns a result with `Meta["presummarized"]="true"`
- WHEN `filter.Apply` is called on that result
- THEN filter returns the result unchanged (no content modification)
- AND `filterMetrics.FilterName` indicates no filter was applied

#### Scenario: Apply still processes non-presummarized results

- GIVEN context_mode is `auto`
- AND a tool result WITHOUT `Meta["presummarized"]`
- WHEN `filter.Apply` is called
- THEN normal filter processing applies
