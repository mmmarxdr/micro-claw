# Test Strengthening Specification (test-strengthening)

## Purpose

Converts four test weaknesses (T1–T4) from the review findings into hard requirements. Each item describes what MUST be asserted; tests that only assert the structural condition (e.g. "if len > limit AND NOT truncated") are not acceptable.

## Requirements

### Requirement: T1 — Truncation Test Makes Direct Assertions

`TestSandboxRun/truncates_large_output` MUST assert `Metrics.Truncated == true` directly, MUST verify the summary contains `[truncated:`, and MUST verify that the head and tail content are present in the summary.

(Previously: assertion was `if len(result.Output) > sb.MaxOutputBytes && !result.Metrics.Truncated { t.Error(...) }` — this only fails when BOTH conditions hold; if output is unexpectedly empty the test passes.)

#### Scenario: Test fails when Truncated is false on oversized output

- GIVEN a `BoundedExec` with `MaxOutputBytes=50` running a command producing >50 bytes
- WHEN the test checks the result
- THEN `t.Error` is triggered if `result.Metrics.Truncated != true`
- AND `t.Error` is triggered if `result.Summary` does not contain `[truncated:`

#### Scenario: Test verifies head content present in summary

- GIVEN the same oversized run
- WHEN the test inspects the summary
- THEN the test asserts the first output line appears in `Summary`

#### Scenario: Test verifies tail content present in summary

- GIVEN the same oversized run with known last-line content
- WHEN the test inspects the summary
- THEN the test asserts the last output line appears in `Summary`

### Requirement: T2 — No Unreachable Error Branches

`TestSandboxRun` MUST NOT contain `if err != nil { t.Errorf(...) }` branches that are structurally unreachable because `BoundedExec.Run` always returns `nil` error.

(Previously: `sandbox_test.go:193-194` had `if err != nil` guard after `sb.Run` which never returns non-nil.)

#### Scenario: Unreachable branch removed

- GIVEN the test file after the fix
- WHEN reviewed statically
- THEN no test case guards on `err != nil` from `BoundedExec.Run` when the contract states it always returns nil

### Requirement: T3 — Timeout Test Has Tight Bounds

The timeout test MUST assert the measured duration is within a tight window of `[timeout × 0.5, timeout × 2.5]` or tighter.

(Previously: window was 50ms–300ms for a 100ms timeout — a broken impl that sleeps 150ms rather than killing passes.)

#### Scenario: Broken impl sleeping past timeout fails

- GIVEN `BoundedExec{Timeout: 100ms}` running `sleep 1`
- AND an impl that does not kill the process (sleeps 150ms instead)
- WHEN duration is checked
- THEN the test fails because 150ms > 100ms × 2.5 = 250ms is still within old window — use `timeout × 1.5` upper bound (150ms) to force failure

#### Scenario: Correct impl killing at timeout passes

- GIVEN `BoundedExec{Timeout: 100ms}` running `sleep 1`
- AND a correct impl that kills at timeout
- WHEN duration is checked
- THEN measured duration is within [50ms, 150ms] and test passes

### Requirement: T4 — Missing Coverage Added

The following test cases MUST exist:

| Test | Location | What it asserts |
|------|----------|----------------|
| PreApply+Apply double-processing | `filter_test.go` | Shell_exec result with `Meta["presummarized"]="true"` passes through `Apply` unchanged |
| LIKE escape | `sqlitestore_test.go` | Query containing `%` only matches rows with literal `%`, not all rows |
| IndexOutput empty ID | `sqlitestore_test.go` | Returns non-nil error |
| IndexOutput empty Content | `sqlitestore_test.go` | Returns non-nil error |

#### Scenario: PreApply+Apply double-processing test exists and fails on broken code

- GIVEN current broken `filter.Apply` (no presummarized guard)
- WHEN test calls `Apply` on a result with `Meta["presummarized"]="true"]` and git-diff-like content
- THEN the broken code modifies the content and the test assertion fails

#### Scenario: LIKE escape test exists and fails on unescaped code

- GIVEN current broken `SearchOutputs` (unescaped `likePattern`)
- WHEN test calls `SearchOutputs` with query `"50%"` and there is one entry with `"50%"` and one without
- THEN the broken code returns multiple rows and the test assertion fails

#### Scenario: IndexOutput validation test exists and fails on unvalidated code

- GIVEN current broken `IndexOutput` (no validation)
- WHEN test calls `IndexOutput` with `ID=""`
- THEN the broken code returns nil and the test assertion fails
