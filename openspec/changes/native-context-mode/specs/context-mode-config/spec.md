# Context Mode Configuration Specification

## Purpose

Provides the configuration structure and defaults for native context-mode token optimization. Controls pre-execution limits, sandbox behavior, and output indexing.

## Requirements

### Requirement: ContextMode Enum

The system MUST support three context mode values: `off`, `conservative`, and `auto`.

#### Scenario: Default mode is off

- GIVEN no `context_mode` field in config
- WHEN config is loaded
- THEN context mode defaults to `off`

#### Scenario: Explicit mode values accepted

- GIVEN a config with `context_mode: auto`
- WHEN config is loaded and validated
- THEN mode is set to `auto`

#### Scenario: Invalid mode rejected

- GIVEN a config with `context_mode: turbo`
- WHEN config is validated
- THEN validation fails with error "unknown context_mode: turbo"

### Requirement: Per-Mode Defaults

The system SHALL apply different defaults based on the active context mode.

#### Scenario: Auto mode defaults

- GIVEN context_mode is `auto`
- WHEN defaults are applied
- THEN ShellMaxOutput is 4096 bytes
- AND FileChunkSize is 2000 bytes
- AND SandboxTimeout is 30s
- AND AutoIndexOutputs is true

#### Scenario: Conservative mode defaults

- GIVEN context_mode is `conservative`
- WHEN defaults are applied
- THEN ShellMaxOutput is 8192 bytes
- AND FileChunkSize is 4000 bytes
- AND SandboxTimeout is 30s
- AND AutoIndexOutputs is true

#### Scenario: Off mode disables features

- GIVEN context_mode is `off`
- WHEN defaults are applied
- THEN pre-execution limits are not applied
- AND sandbox wrapper is bypassed
- AND auto-indexing is disabled

### Requirement: Per-Tool Limit Overrides

The system SHOULD allow per-tool overrides for shell max output and file chunk size via config.

#### Scenario: Shell override applied

- GIVEN config has `context_mode: auto` and `shell_max_output: 2048`
- WHEN shell tool executes
- THEN output is capped at 2048 bytes

#### Scenario: File override applied

- GIVEN config has `context_mode: conservative` and `file_chunk_size: 1000`
- WHEN file tool reads
- THEN chunks are 1000 bytes

### Requirement: Config Validation

The system MUST validate ContextModeConfig fields on load.

#### Scenario: Negative shell limit rejected

- GIVEN config has `shell_max_output: -1`
- WHEN config is validated
- THEN validation fails with error about positive value required

#### Scenario: Zero timeout rejected

- GIVEN config has `sandbox_timeout: 0`
- WHEN config is validated with mode != off
- THEN validation fails with error about positive timeout required
