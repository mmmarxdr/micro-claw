# Filter System (Modified) Specification

## Purpose

Adds pre-execution hook points to the existing post-execution filter system, allowing filters to intercept before tool output enters context.

## MODIFIED Requirements

### Requirement: PreExecute Hook

The filter system MUST support a PreExecute hook that runs before tool execution when context mode is active.

(Previously: Filters only applied post-execution via Apply())

#### Scenario: PreExecute called before tool execution

- GIVEN context_mode is `auto`
- WHEN shell_exec is about to execute
- THEN PreExecute hook is called with tool name and input params
- AND hook can modify execution parameters (e.g., enforce limits)

#### Scenario: PreExecute returns execution hints

- GIVEN PreExecute is called for shell_exec
- WHEN it returns PreExecResult with MaxOutputBytes=2048
- THEN the tool execution respects this limit

#### Scenario: No hook when context mode is off

- GIVEN context_mode is `off`
- WHEN any tool executes
- THEN PreExecute is not called
- AND existing post-execution Apply() behavior is unchanged

### Requirement: PreExecResult Type

The system SHALL define a PreExecResult type that carries execution hints from the pre-execution hook.

#### Scenario: PreExecResult fields populated

- GIVEN PreExecute returns for shell_exec
- THEN PreExecResult has: MaxOutputBytes, ChunkSize, TimeoutOverride, Skip bool

#### Scenario: Skip flag prevents execution

- GIVEN PreExecute returns PreExecResult{Skip: true}
- WHEN the tool would execute
- THEN execution is skipped
- AND a ToolResult is returned indicating skipped

### Requirement: Post-Execution Flow Preserved

The existing post-execution filter Apply() MUST remain unchanged and continue to work as before.

#### Scenario: Post-execution filter still applies

- GIVEN context_mode is `auto`
- AND shell_exec completes
- WHEN result is returned
- THEN post-execution filter.Apply() is still called
- AND filter metrics are still captured
