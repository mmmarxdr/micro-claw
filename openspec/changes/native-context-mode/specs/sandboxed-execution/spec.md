# Sandboxed Execution Specification

## Purpose

Wraps shell command execution in a subprocess sandbox with output capture, byte counting, timeout enforcement, and summary extraction. Returns structured results instead of raw output.

## Requirements

### Requirement: Sandbox Wrapper

The system SHALL wrap exec.Command in a Sandbox struct that captures stdout and stderr through pipes with byte counters.

#### Scenario: Successful command returns summary

- GIVEN a sandbox executing `echo "hello world"`
- WHEN the command completes successfully
- THEN SandboxResult contains the full output in Summary
- AND Metrics.TotalBytes reflects actual output size
- AND Truncated is false

#### Scenario: Large output truncated with summary

- GIVEN a sandbox with MaxOutputBytes=100 executing `seq 1 1000`
- WHEN output exceeds 100 bytes
- THEN Summary contains first N lines + truncation marker + last N lines
- AND Metrics.TotalBytes reflects full output size
- AND Metrics.ShownBytes reflects bytes returned in Summary
- AND Truncated is true

### Requirement: Timeout Enforcement

The system MUST enforce a configurable timeout using context.WithTimeout, killing the subprocess on expiry.

#### Scenario: Command killed on timeout

- GIVEN a sandbox with 2s timeout executing `sleep 10`
- WHEN the timeout elapses
- THEN the subprocess is killed
- AND SandboxResult.IsError is true
- AND error message indicates timeout

#### Scenario: Fast command completes before timeout

- GIVEN a sandbox with 30s timeout executing `echo done`
- WHEN the command completes in <1s
- THEN SandboxResult contains output normally
- AND Truncated is false

### Requirement: Summary Extraction

The system SHALL extract a summary from oversized output: first N lines, a truncation indicator, and last N lines.

#### Scenario: Summary includes context

- GIVEN output of 200 lines with first/last N=5
- WHEN summary is extracted
- THEN Summary contains lines 1-5, a separator like "\n...(190 lines truncated)...\n", and lines 196-200
- AND original line count is preserved in Metrics

#### Scenario: Short output not truncated

- GIVEN output of 10 lines with max 10000 bytes
- WHEN summary is extracted
- THEN Summary is the full output
- AND Truncated is false

### Requirement: Exit Code Capture

The system MUST capture and return the process exit code regardless of success or failure.

#### Scenario: Non-zero exit code captured

- GIVEN a sandbox executing `exit 42`
- WHEN the command completes
- THEN Metrics.ExitCode is 42
- AND SandboxResult.IsError is true

#### Scenario: Zero exit code captured

- GIVEN a sandbox executing `echo ok`
- WHEN the command completes successfully
- THEN Metrics.ExitCode is 0
- AND SandboxResult.IsError is false
