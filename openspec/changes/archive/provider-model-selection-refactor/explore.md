# Exploration — provider-model-selection-refactor

## Problem Statement

Two coupled problems compound each other. First, model selection is structurally broken: there are two independent hardcoded catalogs (`setup.ProviderCatalog` in Go and `KNOWN_MODELS` in TypeScript) that must be kept manually in sync, yet neither is used at runtime for actual API calls — they only populate UI pickers. The `/api/models` endpoint already delegates to per-provider `ListModels()` implementations, but the frontend only uses it for OpenRouter; all other providers fall back to the stale static list. No live refetch happens when the provider tab changes, and there is no free-text model input in Settings (only in the Setup Wizard via `OtherModelSentinel`).

Second, reasoning models silently return empty responses because the OpenRouter stream Delta struct (`openrouter_stream.go:29–40`) only has `Content` and `ToolCalls`. When DeepSeek R1, Xiaomi MiMo, Claude extended thinking, or any model that emits `reasoning`/`reasoning_content` deltas streams back, Go's `json.Unmarshal` silently drops the unknown fields, producing an empty assistant message. The user sees nothing. Both problems share the root cause of static structs and static lists — solving them together is the natural decomposition.

---

## Current State

### Provider/Model Discovery

| Provider | `ListModels()` | Endpoint | Auth | Pagination |
|----------|---------------|----------|------|------------|
| Anthropic | ✅ | `GET /v1/models?limit=100` | `x-api-key` header | No (limit=100, returns all) |
| OpenAI | ✅ | `GET /v1/models` | Bearer token | No |
| Gemini | ✅ | `GET /v1beta/models?key=` | API key in query | No (returns all, filtered to `generateContent`) |
| OpenRouter | ✅ | `GET /api/v1/models` | Bearer token (optional for public read) | No — returns all 342+ models |
| Ollama | ❌ | `GET /api/tags` (local server) | None | No |

`ModelLister` interface exists (`provider.go:133`). `handleListModels` (`web/handler_models.go`) already uses it, but the server only wires one provider as `ModelLister` — whichever is the active provider. There is no `/api/providers/{p}/models` per-provider endpoint.

Frontend gaps:
- `SettingsPage.tsx:113` — `remoteModels` from `/api/models` is only used when `activeProvider === 'openrouter'`. All other providers hit `KNOWN_MODELS`.
- No `refetchOnProviderChange` logic — switching provider tabs doesn't trigger a new fetch.
- No free-text "custom model" fallback in Settings (only in TUI Setup Wizard).
- Model list is not virtualized; at 342 OpenRouter models, no search without filter cap of 50.

Backend catalogs:
- `internal/setup/providers.go:27` — `ProviderCatalog` — static, used ONLY for TUI Setup Wizard and `GET /api/setup/providers` (setup flow). NOT used during actual inference.
- `daimon-frontend/src/schemas/config.ts:95` — `KNOWN_MODELS` — static fallback for Settings page UI.
- Tests that reference `ProviderCatalog`: `internal/web/setup_handlers.go` references it at runtime (not test). No test files found that assert on ProviderCatalog entries specifically.

### Reasoning Token Handling

**OpenRouter wire format** (confirmed from live API + `supported_parameters` field):
- 166 of 342 models declare `reasoning` in `supported_parameters`.
- To receive reasoning tokens, the request must include `"reasoning": {"effort": "high"}` (or similar) OR `"include_reasoning": true`.
- The stream response adds a `reasoning` field alongside `content` in the delta: `{"delta": {"content": "...", "reasoning": "...thinking text..."}}`.
- DeepSeek R1 and Xiaomi MiMo use `reasoning_content` (OpenAI-passthrough format via OpenRouter).
- **Root bug**: `openrouterStreamChunk.Choices[].Delta` struct (line 29–40) has only `Content *string` and `ToolCalls`. No `Reasoning` or `ReasoningContent` field. Go drops unknown fields silently.

**Anthropic extended thinking** (API docs reference):
- Request must include `"thinking": {"type": "enabled", "budget_tokens": N}` in the top-level body.
- Stream events: `content_block_start` with `type: "thinking"`, followed by `content_block_delta` with `delta.type: "thinking_delta"` and `delta.thinking: "..."`.
- `anthropic_stream.go` handles `content_block_start` only for `"tool_use"` type — the `"thinking"` type is NOT handled. `content_block_delta` handles `"text_delta"` and `"input_json_delta"` only — NOT `"thinking_delta"`.

**OpenAI reasoning (o-series via OpenRouter)**:
- Via OpenRouter passthrough, same `reasoning`/`reasoning_content` delta fields apply.
- Direct OpenAI API: o-series exposes `reasoning_tokens` in usage but does NOT expose reasoning text in the stream (private). Only accessible via OpenRouter with `include_reasoning: true`.

**Gemini thinking mode**:
- Gemini 2.0+ thinking models emit a separate `thoughtsContent` field in the response.
- Via streaming: thinking appears as a separate content part with role `"model"` but part-type indicating thought. Exact SSE field: `candidates[0].content.parts[].thought == true`.
- Gemini stream implementation (`gemini_stream.go`) does NOT exist — Gemini uses sync `Chat()` only. No streaming is implemented yet.

**DeepSeek R1 (direct)**:
- `reasoning_content` field in the delta alongside `content`. Same as OpenRouter passthrough.

**Common abstraction across providers**:
The minimal shared interface is: a stream of `(role: reasoning | content, text: string)` token pairs. What differs is only how each provider's wire format encodes them. The internal `StreamEvent` type in `stream.go` can be extended with a new type `StreamEventReasoningDelta` (analogous to `StreamEventTextDelta`) carrying the thinking text.

**WebSocket wire gap**:
`channel/web.go` sends only `token` (text delta), `tool_start`, `tool_done`, `done`, `error`, `turn_start`, `turn_end`. There is no `reasoning_token` message type. The frontend `ChatPage.tsx:386–395` handles `token` messages for streaming. There is no handler for reasoning content. No versioning or capability negotiation exists on the WebSocket.

---

## Options

### For Dynamic Model Discovery

**Option A — Deprecate both catalogs, require live API**
- Remove `KNOWN_MODELS` and `ProviderCatalog`. All model lists come from `ListModels()`.
- Pros: single source of truth, always current.
- Cons: fails completely when provider API is down; Anthropic/OpenAI require valid keys; breaks setup wizard flow (keys not yet entered).
- Complexity: Medium.

**Option B — Keep catalogs as permanent fallback**
- `ListModels()` is called; on failure, serve the static catalog.
- Pros: resilient; good UX when offline or during setup.
- Cons: two catalogs still need maintenance (but infrequently); users may see stale defaults.
- Complexity: Low.

**Option C — Hybrid fetch-on-demand with in-memory cache + static fallback**
- Add `/api/providers/{p}/models` endpoint that calls `ListModels()` per provider, cached in-memory with TTL (e.g., 10 min for Anthropic/OpenAI, 5 min for OpenRouter).
- Frontend fetches per active provider tab, with refetch on provider switch.
- Cache miss (API down) → serve static catalog entry.
- Pros: always fresh when available; resilient; removes the "only OpenRouter" branch in frontend.
- Cons: cache invalidation complexity; TTL must be tuned; adds per-provider endpoints.
- Complexity: Medium.

**Recommendation**: Option C. It solves all three UX gaps (stale lists, no refetch on switch, no non-OpenRouter live search) while maintaining resilience.

**Ollama `ListModels()` gap**:
Ollama needs a new `ListModels()` implementation calling `GET /api/tags` on `p.baseURL`. The response shape is `{"models": [{"name": "llama3.2:latest", ...}]}`. This is a small addition to `ollama.go`.

### For Reasoning Token Streaming + Display

**Option X — Extend Delta with optional reasoning field, emit `StreamEventReasoningDelta`**
- Add `Reasoning *string` and `ReasoningContent *string` to `openrouterStreamChunk.Delta`.
- Add `ThinkingDelta` case to Anthropic stream handler for `thinking_delta` events.
- Add `StreamEventReasoningDelta` to the `StreamEventType` enum in `stream.go`.
- WebSocket: emit new `reasoning_token` message type (backward compat — old clients ignore it).
- Frontend: accumulate reasoning tokens separately, render in a collapsible `<ThinkingBlock>` component before the assistant response text.
- Pros: minimal, generic, mirrors the existing text delta pattern; one abstraction for all providers.
- Cons: request-side must opt-in per provider (send `include_reasoning: true` or Anthropic `thinking` param); not automatic.
- Complexity: Medium.

**Option Y — Separate `ThinkingStream` parallel to content stream**
- Reasoning tokens flow through a separate channel/goroutine.
- Pros: cleanly separates concerns.
- Cons: over-engineered; adds goroutine coordination complexity; no clear advantage over Option X.
- Complexity: High.

**Recommendation**: Option X. It's the minimal change that generalizes across all providers — same `StreamEventReasoningDelta` whether the source is Anthropic `thinking_delta`, OpenRouter `reasoning` field, or DeepSeek `reasoning_content`. The frontend collapsible section is the natural UI primitive (used by Claude.ai, Perplexity, etc.).

---

## Open Questions

1. **Request-side opt-in for reasoning**: Should reasoning always be requested when the model supports it, or should it be a user-controlled toggle per-conversation/model? The `supported_parameters` field in OpenRouter can be used to auto-detect.
2. **Anthropic thinking budget**: `budget_tokens` is required for Anthropic extended thinking. What default? Should it be configurable?
3. **Setup Wizard catalog**: Should `ProviderCatalog` be kept as the TUI wizard's embedded fallback, or should the wizard also call `ListModels()`? The wizard runs before API keys are fully validated — this affects when live fetch is first possible.
4. **Cache backend**: In-memory map (simple, lost on restart) vs SQLite (persistent, survives restart, enables TTL expiry). Given the existing SQLite store, SQLite-backed cache is feasible.
5. **Config validation on startup**: If a user's config references `gpt-5.4` (from old catalog) and the live API doesn't return it, should startup warn, fail, or silently pass? Current `HealthCheck()` doesn't validate model against `ListModels()` output.
6. **Gemini streaming**: Gemini currently has no `ChatStream()`. Should this refactor add Gemini streaming, or defer it? Gemini thinking mode requires streaming to be useful.
7. **OpenRouter `reasoning` toggle in request**: Must be explicitly requested. Who sends it — the agent loop, or auto-detected per model based on `supported_parameters`?

---

## Risks & Unknowns

- **Anthropic/OpenAI `ListModels()` require valid keys**: During setup (before keys are entered), these will fail. The fallback catalog is needed at minimum for the wizard phase.
- **OpenRouter returns 342+ models**: Virtualizing the dropdown is not optional — the current 50-item slice cap in `SettingsPage.tsx:117` is a workaround, not a solution.
- **Reasoning request opt-in**: Sending `include_reasoning: true` to a non-reasoning model may cause a 400 error. Auto-detection from `supported_parameters` is available from OpenRouter but not from direct Anthropic/OpenAI.
- **Backward compat for existing WebSocket clients**: Adding `reasoning_token` message type is additive. Old frontends ignore unknown types — no breaking change.
- **No Gemini `ChatStream()` yet**: Reasoning display for Gemini requires streaming first. Scope question for this change.
- **Xiaomi MiMo specifically**: Listed as `xiaomi/mimo-v2-omni` on OpenRouter (not `mimo-v2-pro` as mentioned in the problem statement). May be a model ID change. Confirm with user.

---

## References

- `internal/provider/openrouter_stream.go:26–40` — Delta struct missing `Reasoning`/`ReasoningContent`
- `internal/provider/anthropic_stream.go:141–168` — Missing `thinking` content block type handling
- `internal/provider/provider.go:133–135` — `ModelLister` interface
- `internal/web/handler_models.go` — `/api/models` single-provider endpoint
- `internal/setup/providers.go:27–109` — Backend hardcoded catalog
- `daimon-frontend/src/schemas/config.ts:95–101` — Frontend `KNOWN_MODELS`
- `daimon-frontend/src/pages/SettingsPage.tsx:113–128` — OpenRouter-only live fetch branch
- `daimon-frontend/src/pages/ChatPage.tsx:386–419` — WebSocket message handler (no reasoning case)
- `internal/channel/web.go:343–366` — `webStreamWriter` (WriteChunk sends `token` type only)
- OpenRouter models API (live): `https://openrouter.ai/api/v1/models` — 342 models, 166 with `reasoning` in `supported_parameters`. Reasoning models include `reasoning` and `include_reasoning` params.
- Anthropic extended thinking API: `content_block_start` with `type: "thinking"`, delta `type: "thinking_delta"`.
- DeepSeek R1 / OpenRouter passthrough: `delta.reasoning_content` field.
