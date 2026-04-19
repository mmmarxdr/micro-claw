# Archive Report — provider-model-selection-refactor
**Archived**: 2026-04-19
**Duration**: single-session SDD cycle
**Artifact store**: openspec + engram

## Summary

This change addressed two structural failures in Daimon's model selection and reasoning handling: (A) hardcoded dual-catalog model discovery that drifted from provider truth, and (B) silent drop of reasoning tokens across all reasoning-capable providers. The solution consolidates both into a single coherent refactor: providers now drive dynamic model discovery via `GET /api/providers/{provider}/models` (live fetch + TTL cache + catalog fallback), reasoning tokens are first-class transport-level events with a new `StreamEventReasoningDelta` type, and the frontend renders a collapsible `ThinkingBlock` that streams reasoning live and auto-collapses when text arrives.

Users now see real-time model discovery scoped to each provider (including OpenRouter's 342-item catalog virtualized), can type custom model IDs with validation hints, and when using reasoning models they see the thinking process stream in a collapsible block before the answer.

## Capabilities delivered

1. **provider-model-discovery** — synced to `openspec/specs/provider-model-discovery/spec.md`
2. **reasoning-stream** — synced to `openspec/specs/reasoning-stream/spec.md`
3. **chat-thinking-ui** — synced to `openspec/specs/chat-thinking-ui/spec.md`

## Timeline

- Explore → Propose → Spec → Design → Tasks → Apply (5 batches) → Verify → Archive

## Apply batches

- **Batch 1** (Phases 1-4): Backend foundation (types, Anthropic map, stream parsers, Ollama ListModels) — ~25 tests
- **Batch 2** (Phases 5-8): Cache + Registry + HTTP endpoint + delete legacy /api/models — 18 tests
- **Batch 3** (Phases 9-11): Health check + agent loop + setup wizard transient — 10 tests
- **Batch 3.5**: Micro-patch reasoning-only Finalize leak — 1 test
- **Batch 4** (Phases 12-13): Frontend ModelPicker + ThinkingBlock — 30 tests
- **Batch 5** (Phases 14-15): Integration tests + cleanup + CI — 7 tests
- **Post-verify**: W-1/W-2 format-string fixes — 2 tests updated

## Final quality gates

- `go vet ./...` : clean
- `golangci-lint run`: clean
- `go test -race ./...` : 20 packages pass
- `vitest run` (frontend): 71 tests pass
- `tsc --noEmit`: clean
- `make ci`: pass

## Follow-ups (deferred, tracked in engram)

- `gemini-streaming-support` — Gemini has no `ChatStream()` today; blocked by this change
- Per-turn reasoning toggle (user control to disable reasoning for single message)
- Ollama context-length lazy probe (`GET /api/show` per-model; skip for MVP)
- 13 pre-existing ESLint errors in unchanged frontend files (mock.ts, StatusBar.tsx, Input.tsx, context files, useWebSocket.ts, mask.ts) — not introduced by this change

## Key decisions preserved in engram

- `sdd/provider-model-selection-refactor/scope` — original scope boundaries
- `sdd/provider-model-selection-refactor/scope-decisions` — 4 initial decisions (hybrid cache, no versioning, typed thinking map, shared ModelPicker component)
- `sdd/provider-model-selection-refactor/post-propose-decisions` — 3 post-propose decisions (no feature flag, @tanstack/react-virtual, hardcoded Anthropic map)
- `sdd/provider-model-selection-refactor/post-design-decisions` — 4 post-design decisions (delete legacy /api/models, two config keys, reasoning auto-enable, registry pattern)
- `sdd/provider-model-selection-refactor/batch-2-decisions` — 401/404 split, skip sync reasoning
- `sdd/provider-model-selection-refactor/anthropic-reasoning-models` — two thinking shapes research
