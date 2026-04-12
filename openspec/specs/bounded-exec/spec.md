# Delta for Sandboxed Execution (bounded-exec)

## Overview

Fixes the false output-preservation contract in `combinedBuf`, renames `Sandbox` to `BoundedExec` to match its actual guarantees (byte-limit + timeout, not filesystem/network/privilege isolation), and propagates the `truncated` flag and exit code via `result.Meta` so callers can read authoritative values.

## MODIFIED Requirements

### Requirement: Sandbox Wrapper

The system SHALL provide a `BoundedExec` struct (renamed from `Sandbox`) that wraps `exec.Command` with byte-limited output capture and timeout enforcement. The type MUST NOT claim process-level isolation (no filesystem, network, or privilege restrictions are applied). The doc comment MUST explicitly document the actual guarantees: byte-limit (`MaxOutputBytes`) and wall-clock timeout (`Timeout`).

(Previously: type was named `Sandbox` with a comment implying broader isolation; `combinedBuf` comment claimed it received "all output even when counting writers truncate" — this was false)

#### Scenario: BoundedExec captures output up to MaxOutputBytes

- GIVEN `BoundedExec{MaxOutputBytes: 100}` running `seq 1 1000`
- WHEN the command produces output exceeding 100 bytes
- THEN `BoundedExecResult.Metrics.Truncated` is `true`
- AND `BoundedExecResult.Output` contains at most `2 × MaxOutputBytes` bytes (LimitedWriter cap)
- AND `BoundedExecResult.Summary` contains the truncation notice `[truncated:`

#### Scenario: LimitedWriter cap prevents unbounded combinedBuf growth

- GIVEN `BoundedExec{MaxOutputBytes: 1000}` running a command that bursts 100 KB at once
- WHEN the first large Write arrives
- THEN `combinedBuf` receives at most `2 × MaxOutputBytes` bytes total across all writes
- AND `BoundedExecResult.Output` length does not exceed `2 × MaxOutputBytes`

#### Scenario: Truncated flag remains false for small output

- GIVEN `BoundedExec{MaxOutputBytes: 4096}` running `echo hello`
- WHEN command completes
- THEN `BoundedExecResult.Metrics.Truncated` is `false`
- AND `BoundedExecResult.Output` equals the full command output

#### Scenario: Successful command returns summary

- GIVEN a `BoundedExec` executing `echo "hello world"`
- WHEN the command completes successfully
- THEN `BoundedExecResult.Summary` contains the output text
- AND `BoundedExecResult.Metrics.ExitCode` is 0

### Requirement: Timeout Enforcement

The system MUST enforce a configurable timeout using `context.WithTimeout`, killing the subprocess on expiry.

(Previously: unchanged — carried forward intact)

#### Scenario: Command killed on timeout

- GIVEN a `BoundedExec` with 2s timeout executing `sleep 10`
- WHEN the timeout elapses
- THEN the subprocess is killed
- AND `BoundedExecResult.Metrics.ExitCode` is non-zero (typically -1)

#### Scenario: Fast command completes before timeout

- GIVEN a `BoundedExec` with 30s timeout executing `echo done`
- WHEN the command completes in <1s
- THEN `BoundedExecResult.Summary` contains "done"
- AND `BoundedExecResult.Metrics.Truncated` is false

### Requirement: Summary Extraction

The system SHALL extract a summary from oversized output: first N lines, a truncation indicator, and last N lines.

(Previously: unchanged — carried forward intact)

#### Scenario: Summary includes head and tail context

- GIVEN output of 200 lines with `KeepFirstN=5` and `KeepLastN=5`
- WHEN summary is extracted
- THEN `Summary` contains lines 1–5, a separator `\n...(N lines truncated)...\n`, and lines 196–200

#### Scenario: Short output not truncated

- GIVEN output of 10 lines with `MaxOutputBytes=10000`
- WHEN summary is extracted
- THEN `Summary` equals the full output
- AND `Metrics.Truncated` is false

### Requirement: Exit Code Capture

The system MUST capture and return the process exit code via `Metrics.ExitCode`.

(Previously: unchanged — carried forward intact)

#### Scenario: Non-zero exit code captured

- GIVEN a `BoundedExec` executing `sh -c "exit 42"`
- WHEN the command completes
- THEN `Metrics.ExitCode` is 42

#### Scenario: Zero exit code captured

- GIVEN a `BoundedExec` executing `echo ok`
- WHEN the command completes successfully
- THEN `Metrics.ExitCode` is 0

### Requirement: Meta Propagation

When `preApplyShell` returns a `ToolResult`, the `Meta` map MUST include `"exit_code"` and `"truncated"` keys with the authoritative values from `BoundedExecResult.Metrics`.

(Previously: `Meta["exit_code"]` was set but `Meta["truncated"]` was absent, leaving callers to infer truncation from filter metrics — which is incorrect for pre-apply intercepted calls)

#### Scenario: Meta contains authoritative truncated flag

- GIVEN `preApplyShell` runs a command that produces output exceeding `ShellMaxOutput`
- WHEN the `ToolResult` is returned
- THEN `result.Meta["truncated"]` equals `"true"`
- AND `result.Meta["exit_code"]` equals the actual exit code string

#### Scenario: Meta truncated false when output fits

- GIVEN `preApplyShell` runs `echo hi`
- WHEN the `ToolResult` is returned
- THEN `result.Meta["truncated"]` equals `"false"`

## REMOVED Requirements

### Requirement: Dead code — `preApplyFileRead`

(Reason: function always returns `(ToolResult{}, false)` — it never intercepts. Phase 2 file chunking is explicitly out of scope. Removing prevents future confusion about `FileChunkSize` having any effect.)
