# Skill Registry

**Delegator use only.** Any agent that launches sub-agents reads this registry to resolve compact rules, then injects them directly into sub-agent prompts. Sub-agents do NOT read this registry or individual SKILL.md files.

**ALWAYS INJECT**: `context-mode` rules are injected into EVERY sub-agent launch regardless of task context. They are not skill-matched — they protect context window from flooding.

See `_shared/skill-resolver.md` for the full resolution protocol.

## User Skills

| Trigger | Skill | Path |
|---------|-------|------|
| PR creation workflow for Agent Teams Lite | branch-pr | ~/.claude/skills/branch-pr/SKILL.md |
| Asks about libraries, frameworks, API references | context7-mcp | ~/.claude/skills/context7-mcp/SKILL.md |
| Helps users discover and install agent skills | find-skills | ~/.agents/skills/find-skills/SKILL.md |
| When writing Go tests, using teatest, or adding test coverage | go-testing | ~/.config/opencode/skills/go-testing/SKILL.md |
| Idiomatic Go patterns, best practices, and conventions | golang-patterns | .agents/skills/golang-patterns/SKILL.md |
| Invoke for goroutines, channels, Go generics, gRPC | golang-pro | .agents/skills/golang-pro/SKILL.md |
| When creating a GitHub issue, reporting a bug, or requesting a feature | issue-creation | ~/.claude/skills/issue-creation/SKILL.md |
| When user says "judgment day", "review adversarial", "dual review" | judgment-day | ~/.claude/skills/judgment-day/SKILL.md |
| When user asks to create a new skill, add agent instructions | skill-creator | ~/.config/opencode/skills/skill-creator/SKILL.md |

## Compact Rules

### go-testing
- Standard Go pattern for multiple test cases (table-driven)
- Write Go unit tests using `testing`
- Test Bubbletea TUI components using `teatest`
- Use golden file testing

### golang-patterns
- Favor simplicity over cleverness; code should be obvious
- Write idiomatic Go patterns for robust applications

### golang-pro
- Run `go vet ./...` before proceeding
- Run `golangci-lint run` and fix all reported issues before proceeding
- Confirm race detector passes before committing (`-race` flag)
- Use table-driven tests and ensure 80%+ coverage

### context7-mcp
- Use context7 to query library docs, specific frameworks
- Verify accurate code generation involving external libraries

### branch-pr
- Branch/PR workflow per issue-first enforcement

### judgment-day
- Review targets independently with blind judge sub-agents
- Apply fixes and re-judge until both pass or escalate

### context-mode (ALWAYS INJECT — not skill-matched)
- Think in Code: write JS/shell to process data via `ctx_execute`, never read raw data into context
- Primary tool: `ctx_batch_execute(commands, queries)` — one call replaces 30+ tool calls
- Blocked: curl, wget, inline HTTP, direct web fetching — use `ctx_fetch_and_index` + `ctx_search`
- Shell output >20 lines → use `ctx_execute(language: "shell", code: "...")`
- File reading for analysis → use `ctx_execute_file(path, language, code)`
- Write artifacts to FILES, never return inline — keep responses under 500 words
- Full rules: `.atl/_shared/context-mode.md`

## Project Conventions

| File | Path | Notes |
|------|------|-------|
| MICROAGENT.md | MICROAGENT.md | Primary AI Context Document, single source of truth |
| TESTS.md | TESTS.md | Defines all tests for the MVP and DoD |
| TUI_IMPROVEMENTS.md | TUI_IMPROVEMENTS.md | TUI-specific improvement plans |
