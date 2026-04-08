# Shell Tool (Modified) Specification

## Purpose

Modifies the existing shell tool to add pre-execution output cap at pipe level and sandbox integration for context-mode optimization.

## MODIFIED Requirements

### Requirement: Pre-Execution Output Cap

The shell tool MUST cap output at the pipe level (not post-read) when context mode is active, controlled by ShellMaxOutput config.

(Previously: Shell tool had a hardcoded 10KB post-read truncation)

#### Scenario: Output capped at pipe level in auto mode

- GIVEN context_mode is `auto` with ShellMaxOutput=4096
- AND shell_exec runs a command producing 50KB of output
- WHEN the command executes
- THEN output is captured up to 4096 bytes at the pipe level
- AND ToolResult.Content contains capped output + truncation notice
- AND Meta includes "truncated": "true"

#### Scenario: No cap when context mode is off

- GIVEN context_mode is `off`
- AND shell_exec runs a command producing 5KB of output
- WHEN the command executes
- THEN output is captured using existing 10KB hardcoded limit
- AND behavior matches current implementation

#### Scenario: Custom shell_max_output override

- GIVEN context_mode is `auto` with shell_max_output=2048
- WHEN shell_exec executes
- THEN pipe cap is 2048 bytes

### Requirement: Sandbox Integration

When context mode is active, the shell tool MUST delegate execution to the sandbox wrapper.

#### Scenario: Sandbox returns SandboxResult

- GIVEN context_mode is `auto`
- WHEN shell_exec runs `ls /tmp`
- THEN execution goes through Sandbox.Run
- AND ToolResult is derived from SandboxResult.Summary

#### Scenario: Sandbox timeout applied

- GIVEN context_mode is `auto` with sandbox_timeout=5s
- WHEN shell_exec runs `sleep 30`
- THEN the command is killed after 5s
- AND ToolResult.IsError is true with timeout message

### Requirement: Backward Compatibility

When context_mode is `off`, the shell tool MUST behave identically to the current implementation.

#### Scenario: Off mode preserves existing behavior

- GIVEN context_mode is `off`
- WHEN shell_exec runs any allowed command
- THEN execution path matches current exec.Command flow
- AND 10KB hardcoded truncation applies
- AND no sandbox is involved
