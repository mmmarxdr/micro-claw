# Config (Modified) Specification

## Purpose

Adds ContextModeConfig struct to the existing AgentConfig, providing YAML-serializable configuration for native context-mode features.

## MODIFIED Requirements

### Requirement: ContextMode Field in Config

The Config struct MUST include a top-level `context_mode` YAML field of type string.

(Previously: No context_mode field existed)

#### Scenario: YAML field parsed

- GIVEN config.yaml has `context_mode: auto`
- WHEN config is loaded
- THEN cfg.ContextMode is "auto"

#### Scenario: Field defaults to off

- GIVEN config.yaml has no context_mode field
- WHEN config is loaded and defaults are applied
- THEN cfg.ContextMode is "off"

### Requirement: ContextModeConfig Struct

The system SHALL define a ContextModeConfig struct embedded in Config.

#### Scenario: Struct fields present

- GIVEN ContextModeConfig definition
- THEN it has: Mode (ContextMode), ShellMaxOutput (int), FileChunkSize (int), SandboxTimeout (Duration), AutoIndexOutputs (bool)

#### Scenario: Defaults applied per mode

- GIVEN context_mode is `auto`
- WHEN defaults are applied
- THEN ShellMaxOutput=4096, FileChunkSize=2000, SandboxTimeout=30s, AutoIndexOutputs=true

### Requirement: Config Validation for Context Mode

The system MUST validate ContextModeConfig when mode is not `off`.

#### Scenario: Invalid mode value rejected

- GIVEN config has `context_mode: turbo`
- WHEN config is validated
- THEN error "unknown context_mode: turbo" is returned

#### Scenario: Positive limits required

- GIVEN config has `shell_max_output: 0` with mode `auto`
- WHEN config is validated
- THEN error about positive shell_max_output is returned

#### Scenario: Off mode skips validation

- GIVEN config has `context_mode: off` with `shell_max_output: 0`
- WHEN config is validated
- THEN no validation error (off mode doesn't enforce limits)
