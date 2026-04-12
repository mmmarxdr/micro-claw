# Delta for Config (config)

## Overview

Removes `FileChunkSize` from `ContextModeConfig` entirely. Any config file that sets this field MUST fail to load with a descriptive error — silent ignoring is prohibited. The `preApplyFileRead` dead code (filter side) is removed in the bounded-exec spec.

## MODIFIED Requirements

### Requirement: ContextModeConfig Fields

`ContextModeConfig` MUST NOT contain a `FileChunkSize` field. YAML configs that include `file_chunk_size` MUST produce a load-time error.

(Previously: `FileChunkSize` was present with a comment "reserved for Phase 2 chunking". The proposal originally said to log a warning; user decision overrides this — hard error on load.)

#### Scenario: Config without FileChunkSize loads successfully

- GIVEN a YAML config with `context_mode` fields but no `file_chunk_size` key
- WHEN the config is loaded
- THEN loading succeeds with no error

#### Scenario: Config with FileChunkSize fails to load

- GIVEN a YAML config that includes `file_chunk_size: 4096`
- WHEN the config is loaded
- THEN loading returns a non-nil error
- AND the error message references `file_chunk_size`
- AND the config is NOT used

#### Scenario: Existing fields are unaffected

- GIVEN a YAML config with valid `shell_max_output`, `sandbox_timeout`, and `auto_index_outputs`
- WHEN the config is loaded
- THEN all three fields parse correctly with no error
