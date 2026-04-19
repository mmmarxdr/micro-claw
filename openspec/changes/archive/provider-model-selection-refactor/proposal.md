# Proposal — provider-model-selection-refactor

## Intent

Model selection in Daimon is structurally broken and reasoning models return empty responses. Two hardcoded catalogs (`setup.ProviderCatalog` in Go + `KNOWN_MODELS` in TypeScript) drift from reality, while `/api/models` only drives the OpenRouter UI — every other provider falls back to stale lists, switching providers does not refetch, and Settings has no custom-model input. Meanwhile, reasoning models (DeepSeek R1, Xiaomi MiMo, Claude extended thinking, o-series) stream empty assistant messages because the stream Delta structs silently drop unknown `reasoning` / `reasoning_content` / `thinking_delta` fields.

Both problems share the same root cause: static structs, static lists, and a single stream channel. Solving them together yields one coherent change: providers become the single source of truth for both "what models do you offer" and "how do you stream your thoughts", the frontend becomes a dynamic consumer, and reasoning-capable models get a first-class collapsible "thinking" UI in chat.

For users, the result is: pick any provider, see ITS real models (searchable, virtualized even for OpenRouter's 342+), type a custom id if you want, and when the model reasons you SEE it stream live in a collapsible block before the answer.

## Scope

### In Scope

**A. Dynamic model discovery (single source of truth)**
- New backend endpoint `GET /api/providers/{provider}/models` — dispatches to the provider's real `ListModels()`, in-memory TTL cache, static catalog fallback on failure (Option C hybrid).
- Implement `ListModels()` for Ollama (`GET /api/tags` against local server) — closes the last provider gap.
- Frontend Settings: refetch on provider tab change, search input for ALL providers, virtualized list (react-window) for OpenRouter-scale lists, free-text custom-model input with validation hint.
- Setup Wizard: call `/api/providers/{p}/models` after API key validation; keep `ProviderCatalog` only as offline fallback for the pre-validation step.
- Remove frontend `KNOWN_MODELS` hardcoded fallback; remove `ProviderCatalog` from inference-path usage.
- Health check extension: when provider exposes `ListModels()`, validate configured model against real list on startup — surface actionable error (`model X not found for provider Y`).

**B. Reasoning token streaming + collapsible chat UI**
- Extend `openrouterStreamChunk.Delta` with `Reasoning *string` + `ReasoningContent *string`.
- Extend `anthropic_stream.go` to handle `content_block_start` type=`thinking` and `content_block_delta` type=`thinking_delta`.
- Introduce new stream event `StreamEventReasoningDelta` (parallel to `StreamEventTextDelta`).
- New WebSocket message type `reasoning_token` (additive — old clients ignore unknown types).
- Frontend: `ThinkingBlock` component — collapsible, streams reasoning tokens live, auto-collapses when main response text arrives.
- Auto-enable reasoning when model metadata declares support (`supported_parameters` for OpenRouter). For Anthropic extended thinking, send `thinking: {type: enabled, budget_tokens: N}` with default `10000`, configurable via `providers.anthropic.thinking_budget_tokens` in `~/.daimon/config.yaml`.

### Out of Scope

- **Gemini streaming + reasoning display** — deferred to follow-up `gemini-streaming-support`. Gemini has no `ChatStream()` today; adding it is a substantial independent change. Kanon roadmap item tracked in parallel.
- SQLite-backed model cache (in-memory TTL is sufficient for MVP).
- Exposing `thinking_budget_tokens` in the Settings UI (yaml-configurable is sufficient for this change).
- Per-conversation reasoning toggle override (auto-detect is the only activation path for MVP).
- Multi-provider fallback rewrite (untouched — reasoning events propagate through existing `fallback.go`).

## Capabilities

### New Capabilities

- `provider-model-discovery`: Dynamic per-provider model listing with live API fetch, in-memory TTL cache, and static catalog fallback. Covers `/api/providers/{p}/models`, `ModelLister` expansion to Ollama, and startup validation of configured models against real lists.
- `reasoning-stream`: Transport-level abstraction for reasoning tokens across providers. Covers `StreamEventReasoningDelta`, Delta-struct extensions for OpenRouter and Anthropic, auto-activation rules based on model metadata, and Anthropic `thinking_budget_tokens` config.
- `chat-thinking-ui`: Frontend chat treatment for reasoning output. Covers the `reasoning_token` WebSocket message, `ThinkingBlock` collapsible component, auto-collapse on first text token, and dynamic model picker with search + virtualization + custom-input.

### Modified Capabilities

- None. Existing specs (`agent-loop`, `config`, etc.) do not constrain provider discovery, stream event types, or WebSocket frames at the requirement level. All spec-level behavior introduced here is new.

## Approach

### Dynamic model discovery — Option C (hybrid)

- New router `/api/providers/{provider}/models` delegates to a `ModelLister` registry keyed by provider id, not by "active provider".
- In-process cache with per-provider TTL (10 min for Anthropic/OpenAI/OpenRouter, 5 min for Ollama given local changes, 5 min for Gemini).
- On `ListModels()` error (API down, invalid key, network), fall back to `ProviderCatalog` entry for the same provider; response includes `source: "live" | "cache" | "fallback"` so frontend can surface staleness if needed.
- Frontend fetches keyed by active provider tab; react-query keys include provider id so switching is a new query, not a refetch.

### Reasoning streaming + UI — Option X (extend Delta, generic stream event)

- `provider` layer: each stream implementation maps its wire format (`delta.reasoning`, `delta.reasoning_content`, `content_block_delta.thinking_delta`) to a unified `StreamEvent{Type: ReasoningDelta, Text: string}`.
- `agent/loop.go`: treat reasoning deltas as non-terminal events that do not contribute to message content; forward to the channel writer.
- `channel/web.go`: `webStreamWriter` gains `WriteReasoning(s string)` emitting `{type: "reasoning_token", data: s}` frames.
- Frontend chat state: separate `reasoningBuffer` from `textBuffer`; `ThinkingBlock` renders `reasoningBuffer` expanded while `textBuffer` is empty, auto-collapses on first text token, remains clickable to re-expand.
- Auto-enable: before calling a provider, inspect cached model metadata (`supported_parameters` from OpenRouter, static provider hint list for Anthropic reasoning families) and inject the per-provider activation payload (`include_reasoning: true`, `thinking: {...}`, etc.).

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `internal/provider/provider.go` | Modified | `StreamEvent` gains `ReasoningDelta` type; `ModelLister` expanded registry access |
| `internal/provider/openrouter_stream.go` | Modified | Delta struct gets `Reasoning` + `ReasoningContent` fields; stream mapper emits reasoning events |
| `internal/provider/anthropic_stream.go` | Modified | Handle `thinking` content block + `thinking_delta`; inject `thinking: {type, budget_tokens}` on request |
| `internal/provider/anthropic.go` | Modified | Read `thinking_budget_tokens` from config; auto-enable when model in reasoning family |
| `internal/provider/ollama.go` | New method | `ListModels()` against `GET /api/tags` |
| `internal/provider/fallback.go` | Modified (minimal) | Propagate reasoning events through fallback wrapper |
| `internal/web/handler_models.go` | Modified | Add `/api/providers/{p}/models` handler, TTL cache, fallback logic |
| `internal/web/server.go` | Modified | Register new route and cache instance |
| `internal/channel/web.go` | Modified | `webStreamWriter.WriteReasoning`; new `reasoning_token` frame type |
| `internal/agent/loop.go` | Modified | Forward reasoning stream events without accumulating into final message |
| `internal/setup/providers.go` | Modified | `ProviderCatalog` reduced to offline fallback; new helper to merge live + fallback |
| `internal/setup/` (wizard) | Modified | Call `/api/providers/{p}/models` after key validation |
| `internal/config/config.go` | Modified | `Providers.Anthropic.ThinkingBudgetTokens *int` with default 10000 |
| `daimon-frontend/src/schemas/config.ts` | Modified | Remove `KNOWN_MODELS` block |
| `daimon-frontend/src/pages/SettingsPage.tsx` | Modified | Refetch-on-provider-change, search, virtualized list, custom-model input |
| `daimon-frontend/src/pages/SetupWizardPage.tsx` | Modified | Dynamic model fetch after key validation |
| `daimon-frontend/src/pages/ChatPage.tsx` | Modified | Handle `reasoning_token` WebSocket frames; drive `ThinkingBlock` |
| `daimon-frontend/src/components/chat/ThinkingBlock.tsx` | New | Collapsible reasoning viewer with auto-collapse |
| `daimon-frontend/package.json` | Modified | Add `react-window` (+ types) |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Anthropic/OpenAI `ListModels()` fails during setup (no key yet) | High | Keep `ProviderCatalog` as pre-key offline fallback in setup wizard; response exposes `source: "fallback"` |
| OpenRouter 342+ models crash an unvirtualized dropdown | High | react-window virtualization is a hard requirement in spec; block merge if not present |
| Sending `include_reasoning: true` to a non-reasoning model → 400 error | Medium | Only inject activation params when model metadata declares reasoning support; default to off |
| Anthropic extended thinking cost explosion | Medium | Default `budget_tokens: 10000`; configurable; log token usage per turn |
| New `reasoning_token` frames break old frontend builds | Low | Additive message type; unknown-type handler already ignores unrecognized frames |
| Startup model validation blocks existing users whose config references removed model ids | Medium | Warn (not fail) on first run; provide one-line fix suggestion using real list |
| TTL cache serves outdated models when provider updates mid-session | Low | 5–10 min TTL + `?refresh=true` query param for manual bust |
| Ollama local server unreachable at startup | Medium | `ListModels()` returns provider-specific empty list with clear error; frontend falls back to custom-input |

## Rollback Plan

The change is additive at the transport and config level. Rollback strategy:

1. **Backend**: revert commits in `internal/provider/*_stream.go`, `internal/web/handler_models.go`, `internal/channel/web.go`, `internal/agent/loop.go`. The old `/api/models` endpoint stays untouched throughout — removing the new `/api/providers/{p}/models` route does not affect the legacy path.
2. **Config**: `providers.anthropic.thinking_budget_tokens` is optional with a default; absent/removed → reasoning auto-enable becomes a no-op.
3. **Frontend**: revert `SettingsPage.tsx` + `ChatPage.tsx` + delete `ThinkingBlock.tsx`. Re-introduce `KNOWN_MODELS` block from git history if needed. React-query key change forces refetch — no stale cache poisoning on revert.
4. **DB**: no schema changes, no migration needed.
5. **Feature flag**: a single config flag `providers.dynamic_model_discovery: true` (default true) can gate the new endpoint if a hot rollback is needed without a redeploy — spec will define it.

## Dependencies

- `react-window` (and `@types/react-window`) added to `daimon-frontend`.
- No new Go dependencies; uses stdlib `net/http` + existing provider HTTP clients.
- Context7 verification required for `react-window` current API before spec.

## Success Criteria

- [ ] Switching provider tabs in Settings triggers a new fetch; each provider shows its OWN model list (not `KNOWN_MODELS`).
- [ ] `/api/providers/openrouter/models` returns 300+ live models when online; serves cached/fallback within 5ms on cache hit.
- [ ] Ollama `ListModels()` returns the installed models from a running local daemon.
- [ ] OpenRouter model picker renders smoothly at 342+ items (virtualized) with working search.
- [ ] Custom-model input accepts arbitrary strings and round-trips through save.
- [ ] Health check on startup logs a warning naming the missing model when config references a model not in the live list.
- [ ] Invoking DeepSeek R1 or Xiaomi MiMo shows streaming reasoning tokens in a collapsible block BEFORE the main response; block auto-collapses on first text token.
- [ ] Invoking Claude with extended thinking shows the same UI and respects `thinking_budget_tokens`.
- [ ] Old frontend bundle continues to work against the new backend (no `reasoning_token` handler → simply no thinking UI, response still renders).
- [ ] `go test ./...` + `vitest run` pass; race detector clean on provider changes.
- [ ] Setup wizard completes for a new user without hitting `ProviderCatalog` once a valid key is entered.

## Follow-ups (tracked separately)

- `gemini-streaming-support`: add `ChatStream()` to Gemini provider, then wire reasoning (thoughts) display. Blocked by the current change's stream-event contract — should reuse `StreamEventReasoningDelta` as-is.
- Per-conversation reasoning toggle override (user wants ability to disable reasoning on a specific turn for speed).
- Settings UI surface for `thinking_budget_tokens` (once the yaml-only path is validated).
- SQLite-backed model cache if in-memory TTL proves insufficient under multi-process deployments.
