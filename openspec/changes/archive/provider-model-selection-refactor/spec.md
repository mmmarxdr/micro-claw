# Spec — provider-model-selection-refactor

## Scope recap

This change consolidates two structurally broken behaviors into a single coherent refactor: (A) provider-agnostic dynamic model discovery — replacing the dual-hardcoded-catalog problem (`ProviderCatalog` in Go + `KNOWN_MODELS` in TypeScript) with a live-fetch-per-provider endpoint backed by an in-memory TTL cache and a static fallback; and (B) first-class reasoning token streaming — extending the stream layer to emit `ReasoningDelta` events distinct from `TextDelta`, propagating them to the WebSocket channel, and rendering them as a live-streaming collapsible `ThinkingBlock` in the chat UI. All requirements are new (no existing spec overrides). See `proposal.md` for full context, affected areas, and rollback plan.

---

## Capability: provider-model-discovery

### Requirements

**REQ-PMD-1** The system MUST expose `GET /api/providers/{provider}/models` returning the model list for the named provider.

**REQ-PMD-2** The endpoint MUST call `ListModels()` on the `ModelLister` registry entry for the given provider ID; if no key is configured for that provider the endpoint MUST return HTTP 401 with a JSON body `{"error": "provider {name}: no API key configured"}`.

**REQ-PMD-3** Each provider's model list MUST be cached in-memory with a configurable TTL: 10 min for Anthropic, OpenAI, OpenRouter, Gemini; 5 min for Ollama (local — changes frequently).

**REQ-PMD-4** On a cache hit the endpoint MUST respond within 5 ms and MUST set response header `X-Source: cache`.

**REQ-PMD-5** When `ListModels()` returns an error AND a prior cached response exists, the endpoint MUST serve the stale cached response and MUST set header `X-Source: cache-stale`.

**REQ-PMD-6** When `ListModels()` returns an error AND no cache exists, the endpoint MUST serve the static `ProviderCatalog` fallback for that provider and MUST set header `X-Source: fallback`.

**REQ-PMD-7** The endpoint MUST accept an optional query parameter `?refresh=true`; when present it MUST bypass the cache and re-invoke `ListModels()`, updating the cache on success.

**REQ-PMD-8** The Ollama provider MUST implement `ListModels()` by issuing `GET /api/tags` against the configured base URL and mapping the response to `[]ProviderModel`.

**REQ-PMD-9** On startup, when a provider implements `ListModels()`, the health check MUST call it and validate the currently configured model ID against the returned list. If not found, it MUST log a warning in the format: `[daimon] WARNING: model "{model}" not found in provider "{provider}" live list — run daimon config to update`. Startup MUST NOT be blocked (warn, not fail).

**REQ-PMD-10** The response body MUST be a JSON object: `{"models": [...], "source": "live"|"cache"|"cache-stale"|"fallback", "cached_at": "<RFC3339 or null>"}`.

**REQ-PMD-11** The frontend Settings page MUST call `GET /api/providers/{provider}/models` when the active provider tab changes, using a `react-query` cache key that includes the provider ID — switching providers MUST trigger a new fetch, not a re-render of the previous list.

**REQ-PMD-12** The models dropdown in Settings MUST be virtualized using `@tanstack/react-virtual` when the list length exceeds 50 items, to handle OpenRouter's 342+ model catalog without DOM thrashing.

**REQ-PMD-13** The Settings model picker MUST include a search input that filters the model list by substring match on both model ID and display name; filtering MUST be client-side on the cached react-query response.

**REQ-PMD-14** The Settings model picker MUST include a free-text custom model ID input. If the user types a value not present in the fetched list, a hint MUST appear ("Custom model ID — not validated against provider list") and the value MUST still be saveable.

**REQ-PMD-15** The Setup Wizard MUST call `GET /api/providers/{provider}/models` AFTER API key validation succeeds, to populate the model picker from the live list rather than `ProviderCatalog`.

**REQ-PMD-16** `ProviderCatalog` MUST remain in `internal/setup/providers.go` exclusively as an offline fallback; it MUST NOT be referenced in the inference or agent-loop path.

**REQ-PMD-17** `KNOWN_MODELS` in `daimon-frontend/src/schemas/config.ts` MUST be removed; any remaining references in the frontend MUST fall back to the `GET /api/providers/{p}/models` response.

### Scenarios

#### Scenario PMD-1a: Live fetch returns models
- **GIVEN** the Anthropic API key is configured
- **AND** the in-memory cache for `anthropic` is empty
- **WHEN** `GET /api/providers/anthropic/models` is called
- **THEN** the handler MUST invoke `anthropic.ListModels()`
- **AND** the response MUST have HTTP 200, `X-Source: live`, and `source: "live"` in the body
- **AND** the result MUST be stored in the cache with the configured TTL

#### Scenario PMD-1b: Cache hit — fast path
- **GIVEN** the cache for `openrouter` was populated 3 minutes ago (TTL is 10 min)
- **WHEN** `GET /api/providers/openrouter/models` is called
- **THEN** the response MUST be served from cache within 5 ms
- **AND** `X-Source: cache` MUST be present in the response headers

#### Scenario PMD-1c: Upstream failure with existing cache
- **GIVEN** the cache for `openrouter` holds a response from 8 minutes ago
- **AND** the OpenRouter API is unreachable
- **WHEN** `GET /api/providers/openrouter/models` is called
- **THEN** the handler MUST return the stale cached response
- **AND** `X-Source: cache-stale` MUST be set

#### Scenario PMD-1d: Upstream failure, no cache — fallback
- **GIVEN** the cache for `anthropic` is empty
- **AND** `anthropic.ListModels()` returns an error
- **WHEN** `GET /api/providers/anthropic/models` is called
- **THEN** the response MUST contain the `ProviderCatalog` entries for `anthropic`
- **AND** `X-Source: fallback` MUST be set
- **AND** HTTP 200 MUST be returned (graceful degradation, not an error)

#### Scenario PMD-1e: No API key — 401
- **GIVEN** no API key is configured for `openai`
- **WHEN** `GET /api/providers/openai/models` is called
- **THEN** the response MUST be HTTP 401
- **AND** the body MUST contain `"provider openai: no API key configured"`

#### Scenario PMD-1f: Manual cache bust
- **GIVEN** a cache hit exists for `anthropic`
- **WHEN** `GET /api/providers/anthropic/models?refresh=true` is called
- **THEN** `ListModels()` MUST be invoked regardless of cache state
- **AND** on success the cache MUST be updated with a fresh TTL

#### Scenario PMD-2a: Ollama ListModels
- **GIVEN** Ollama is running at the configured base URL (default `http://localhost:11434`)
- **WHEN** `GET /api/providers/ollama/models` is called
- **THEN** the handler MUST issue `GET {baseURL}/api/tags`
- **AND** the response MUST list all locally installed Ollama models

#### Scenario PMD-3a: Startup health check — model not found
- **GIVEN** the configured model is `claude-opus-x-99` for provider `anthropic`
- **AND** `anthropic.ListModels()` returns a list that does NOT include `claude-opus-x-99`
- **WHEN** daimon starts
- **THEN** a WARNING MUST be logged: `model "claude-opus-x-99" not found in provider "anthropic" live list`
- **AND** daimon MUST continue starting normally (no panic, no exit)

#### Scenario PMD-4a: Frontend — switching provider refetches models
- **GIVEN** the user is on the Settings page with OpenRouter active
- **AND** the models list has loaded with 342 items
- **WHEN** the user switches to the "anthropic" provider tab
- **THEN** the frontend MUST call `GET /api/providers/anthropic/models`
- **AND** the models dropdown MUST re-render with the Anthropic response
- **AND** the previously selected model value MUST be cleared unless it is present in the new list

#### Scenario PMD-4b: Search filters the model list
- **GIVEN** the OpenRouter model list is loaded (342 items)
- **WHEN** the user types "deepseek" in the search input
- **THEN** only models whose ID or display name contains "deepseek" (case-insensitive) MUST be shown
- **AND** the list MUST update as the user types (no submit required)

#### Scenario PMD-4c: Custom model ID accepted
- **GIVEN** the model list for `anthropic` is loaded
- **WHEN** the user types `claude-opus-99-preview` (not in list) into the custom model input
- **THEN** a hint MUST appear: "Custom model ID — not validated against provider list"
- **AND** clicking Save MUST persist the value to config without error

#### Scenario PMD-5a: Setup Wizard uses live list after key validation
- **GIVEN** the user has entered a valid Anthropic API key in the Setup Wizard
- **WHEN** key validation succeeds
- **THEN** the Wizard MUST call `GET /api/providers/anthropic/models`
- **AND** the model picker MUST show the live response (not `ProviderCatalog`)

---

## Capability: reasoning-stream

### Requirements

**REQ-RS-1** `provider.StreamEventType` MUST include a new variant `ReasoningDelta` distinct from `TextDelta`.

**REQ-RS-2** The OpenRouter stream parser MUST deserialize `delta.reasoning` and `delta.reasoning_content` fields from each SSE chunk and emit `StreamEvent{Type: ReasoningDelta, Text: value}` when either field is non-empty.

**REQ-RS-3** The Anthropic stream parser MUST handle `content_block_start` with `type: "thinking"` by opening a reasoning accumulation context, and `content_block_delta` with `type: "thinking_delta"` by emitting `StreamEvent{Type: ReasoningDelta, Text: delta.thinking}`.

**REQ-RS-4** `agent/loop.go` MUST forward `ReasoningDelta` events to the channel writer without accumulating them into the final assistant message content.

**REQ-RS-5** The fallback provider wrapper in `internal/provider/fallback.go` MUST propagate `ReasoningDelta` events transparently without filtering or modification.

**REQ-RS-6** Before issuing a chat request, the provider layer MUST check whether the selected model supports reasoning using the following auto-detection rules:
  - **OpenRouter**: check `supported_parameters` field in the cached model metadata — if it contains `"reasoning"`, reasoning is supported.
  - **Anthropic**: look up model ID in the hardcoded capability map `AnthropicThinkingCapability` (see REQ-RS-7).
  - If reasoning is supported, inject the provider-specific activation payload automatically (no user toggle required).

**REQ-RS-7** `internal/provider/anthropic.go` MUST define a capability map `AnthropicThinkingCapability map[string]thinkingShape` where `thinkingShape` is one of `thinkingNone | thinkingAdaptive | thinkingManual`, with the following entries as of 2026-04-19:
  - `thinkingAdaptive`: `claude-opus-4-7`
  - `thinkingManual`: `claude-opus-4-6`, `claude-sonnet-4-6`, plus legacy IDs (`claude-sonnet-4-5-20250929`, `claude-opus-4-5-20251101`, `claude-opus-4-1-20250805`)
  - All other model IDs MUST map to `thinkingNone` (i.e. absence from the map implies no thinking)
  - `claude-haiku-4-5-20251001` explicitly MUST NOT appear in the map (does not support thinking)

**REQ-RS-8** For models with `thinkingAdaptive`, the activation payload MUST be:
  ```json
  {"type": "adaptive", "effort": "<thinking_effort>"}
  ```
  where `thinking_effort` reads from `providers.anthropic.thinking_effort` in config (default `"medium"`, valid: `"low" | "medium" | "high"`).

**REQ-RS-9** For models with `thinkingManual`, the activation payload MUST be:
  ```json
  {"type": "enabled", "budget_tokens": <thinking_budget_tokens>}
  ```
  where `thinking_budget_tokens` reads from `providers.anthropic.thinking_budget_tokens` in config (default `10000`).

**REQ-RS-10** `internal/config/config.go` MUST add:
  - `Providers.Anthropic.ThinkingBudgetTokens *int` (default `10000`)
  - `Providers.Anthropic.ThinkingEffort string` (default `"medium"`)

**REQ-RS-11** Sending the `thinking` activation payload to a model mapped to `thinkingNone` MUST NOT happen — the provider layer MUST skip injection for those models (prevents 400 errors from the Anthropic API).

**REQ-RS-12** For OpenRouter, when a model has `"reasoning"` in `supported_parameters`, the activation payload MUST be `{"include_reasoning": true}` appended to the request body.

### Scenarios

#### Scenario RS-1a: OpenRouter reasoning delta parsed
- **GIVEN** the selected model is a DeepSeek R1 variant on OpenRouter
- **AND** an SSE chunk arrives with `delta: {"reasoning_content": "step 1..."}`
- **WHEN** the stream parser processes the chunk
- **THEN** it MUST emit `StreamEvent{Type: ReasoningDelta, Text: "step 1..."}`
- **AND** it MUST NOT emit a `TextDelta` for the same text

#### Scenario RS-1b: OpenRouter reasoning field takes precedence
- **GIVEN** a chunk contains both `delta.reasoning` and `delta.content`
- **WHEN** the stream parser processes the chunk
- **THEN** BOTH a `ReasoningDelta` (for `delta.reasoning`) AND a `TextDelta` (for `delta.content`) MUST be emitted as separate events

#### Scenario RS-2a: Anthropic thinking block opened
- **GIVEN** the selected model is `claude-opus-4-7`
- **AND** a `content_block_start` event arrives with `content_block.type = "thinking"`
- **WHEN** the Anthropic stream parser processes it
- **THEN** the parser MUST enter reasoning-accumulation mode

#### Scenario RS-2b: Anthropic thinking_delta emitted
- **GIVEN** the parser is in reasoning-accumulation mode
- **AND** a `content_block_delta` arrives with `delta.type = "thinking_delta"` and `delta.thinking = "let me think..."`
- **WHEN** the parser processes it
- **THEN** it MUST emit `StreamEvent{Type: ReasoningDelta, Text: "let me think..."}`

#### Scenario RS-3a: Reasoning not injected for non-reasoning model
- **GIVEN** the selected Anthropic model is `claude-haiku-4-5-20251001`
- **WHEN** a chat request is prepared
- **THEN** NO `thinking` field MUST be added to the request body

#### Scenario RS-3b: Adaptive thinking injected for Opus 4.7
- **GIVEN** the selected model is `claude-opus-4-7`
- **AND** `providers.anthropic.thinking_effort` is `"high"`
- **WHEN** a chat request is prepared
- **THEN** the request body MUST contain `"thinking": {"type": "adaptive", "effort": "high"}`
- **AND** it MUST NOT contain `"budget_tokens"`

#### Scenario RS-3c: Manual thinking injected for Opus 4.6
- **GIVEN** the selected model is `claude-opus-4-6`
- **AND** `providers.anthropic.thinking_budget_tokens` is `15000`
- **WHEN** a chat request is prepared
- **THEN** the request body MUST contain `"thinking": {"type": "enabled", "budget_tokens": 15000}`

#### Scenario RS-4a: ReasoningDelta forwarded by agent loop
- **GIVEN** the provider emits a `ReasoningDelta` event during streaming
- **WHEN** the agent loop processes the event
- **THEN** it MUST call `channelWriter.WriteReasoning(text)` on the channel
- **AND** it MUST NOT append the reasoning text to the assistant message content

#### Scenario RS-4b: ReasoningDelta passes through fallback wrapper
- **GIVEN** the fallback provider wraps Anthropic
- **AND** Anthropic emits a `ReasoningDelta` event
- **WHEN** the fallback wrapper processes the event stream
- **THEN** the `ReasoningDelta` MUST be emitted on the output channel unchanged

#### Scenario RS-5a: OpenRouter reasoning activation injected
- **GIVEN** the selected model has `"reasoning"` in its `supported_parameters`
- **WHEN** a chat request is constructed for OpenRouter
- **THEN** the request body MUST include `"include_reasoning": true`

---

## Capability: chat-thinking-ui

### Requirements

**REQ-CTU-1** The WebSocket protocol MUST support a new message type `reasoning_token` with payload `{"type": "reasoning_token", "data": "<reasoning text fragment>"}`. Old clients that do not handle this type MUST be unaffected (existing unknown-type guard in `ChatPage.tsx` already ignores unrecognized frames).

**REQ-CTU-2** `internal/channel/web.go` MUST implement `WriteReasoning(s string)` on `webStreamWriter`, emitting `reasoning_token` frames to the WebSocket connection.

**REQ-CTU-3** The chat frontend MUST maintain a `reasoningBuffer` state separate from the `textBuffer`; `reasoning_token` frames MUST append to `reasoningBuffer`, `token` frames MUST append to `textBuffer`.

**REQ-CTU-4** A `ThinkingBlock` component (`daimon-frontend/src/components/chat/ThinkingBlock.tsx`) MUST render the `reasoningBuffer` content. It MUST be displayed above the assistant message bubble while reasoning is in progress.

**REQ-CTU-5** While `textBuffer` is empty and `reasoningBuffer` is non-empty, `ThinkingBlock` MUST be in the expanded state, streaming content live.

**REQ-CTU-6** When the first `token` (text) frame arrives for the current turn, `ThinkingBlock` MUST auto-collapse and display a summary line: "Thought for Xs" where X is the elapsed time in seconds from first `reasoning_token` to first `token`.

**REQ-CTU-7** The user MUST be able to click (or press Enter) on the collapsed `ThinkingBlock` to toggle it back to the expanded state; clicking the expanded block MUST collapse it again.

**REQ-CTU-8** The `ThinkingBlock` MUST be keyboard-accessible: it MUST be focusable via Tab and MUST respond to Enter/Space to toggle expand/collapse.

**REQ-CTU-9** After a turn completes, the `ThinkingBlock` for that turn MUST remain rendered and toggleable in the chat history (not discarded on turn end).

**REQ-CTU-10** If a turn produces NO `reasoning_token` frames, `ThinkingBlock` MUST NOT be rendered for that turn.

**REQ-CTU-11** The `ThinkingBlock` collapse/expand animation SHOULD use a CSS height transition (not JS-based layout shift) to avoid jank during rapid token streaming.

### Scenarios

#### Scenario CTU-1a: reasoning_token frames populate ThinkingBlock
- **GIVEN** the assistant is streaming reasoning for the current turn
- **WHEN** `reasoning_token` frames arrive on the WebSocket
- **THEN** `ThinkingBlock` MUST be visible above the assistant message area
- **AND** its content MUST update in real-time with each incoming fragment

#### Scenario CTU-1b: Auto-collapse on first text token
- **GIVEN** `ThinkingBlock` is expanded and has been streaming for 4 seconds
- **WHEN** the first `token` frame arrives
- **THEN** `ThinkingBlock` MUST collapse
- **AND** the summary "Thought for 4s" MUST be displayed
- **AND** the main message streaming MUST begin rendering below

#### Scenario CTU-1c: Manual re-expand after auto-collapse
- **GIVEN** `ThinkingBlock` is in the auto-collapsed state ("Thought for 4s")
- **WHEN** the user clicks the collapsed block
- **THEN** it MUST expand to show the full reasoning content
- **AND** the main message text MUST remain visible below

#### Scenario CTU-1d: Enter key toggles block
- **GIVEN** `ThinkingBlock` is focused (via Tab)
- **WHEN** the user presses Enter
- **THEN** the block MUST toggle between expanded and collapsed states

#### Scenario CTU-1e: ThinkingBlock persists in chat history
- **GIVEN** a completed turn that included reasoning
- **WHEN** the user scrolls up to a previous turn
- **THEN** that turn's `ThinkingBlock` MUST be present and toggleable
- **AND** the full reasoning content MUST be available on expand

#### Scenario CTU-1f: No ThinkingBlock for non-reasoning turns
- **GIVEN** a turn where no `reasoning_token` frames were received
- **THEN** NO `ThinkingBlock` MUST be rendered for that turn

#### Scenario CTU-2a: Old frontend — reasoning_token ignored gracefully
- **GIVEN** an old frontend bundle without `reasoning_token` handling
- **AND** the backend sends `reasoning_token` frames during a turn
- **WHEN** the WebSocket message handler processes the frames
- **THEN** the unknown type MUST be silently ignored
- **AND** the assistant text response MUST still render correctly

#### Scenario CTU-3a: WebSocket WriteReasoning emits correct frame
- **GIVEN** the agent loop calls `channelWriter.WriteReasoning("step A")`
- **THEN** the WebSocket connection MUST receive a frame with `{"type": "reasoning_token", "data": "step A"}`

---

## Open questions

None. All scope, library, and Anthropic model-shape decisions were resolved prior to spec phase (see `post-propose-decisions` and `anthropic-reasoning-models` in engram).

---

## Test plan summary

### Backend

- **Stream parsers** (`openrouter_stream_test.go`, `anthropic_stream_test.go`): table-driven tests covering `ReasoningDelta` emission for each wire-format variant (`delta.reasoning`, `delta.reasoning_content`, `thinking_delta`). Include cases where reasoning and text arrive in the same chunk.
- **Anthropic thinking injection** (`anthropic_test.go`): table-driven test over `AnthropicThinkingCapability` map — verify adaptive payload shape for `claude-opus-4-7`, manual shape for `claude-opus-4-6`/`claude-sonnet-4-6`, no payload for `claude-haiku-4-5-20251001`.
- **Models endpoint** (`handler_models_test.go`): HTTP handler tests covering live fetch (200 + `X-Source: live`), cache hit (< 5 ms + `X-Source: cache`), stale fallback (`X-Source: cache-stale`), catalog fallback (`X-Source: fallback`), no-key 401, `?refresh=true` bust.
- **Ollama ListModels** (`ollama_test.go`): mock HTTP server for `/api/tags`; verify mapping to `[]ProviderModel`.
- **Channel WriteReasoning** (`web_test.go`): assert emitted WebSocket frame is `{"type":"reasoning_token","data":"..."}`.
- **Agent loop** (`loop_test.go`): assert `ReasoningDelta` events invoke `WriteReasoning` and are NOT appended to message content.
- All backend tests MUST pass `go test -race ./...`.

### Frontend

- **`ThinkingBlock` component** (`ThinkingBlock.test.tsx`): unit tests for expand/collapse toggle, auto-collapse on first text token, "Thought for Xs" label, keyboard accessibility (Tab focus, Enter toggle).
- **Settings model picker** (`SettingsPage.test.tsx`): integration test — switching provider tab triggers new `GET /api/providers/{p}/models` call; search input filters list; custom model input shows hint; virtualized list renders for 342-item mock.
- **ChatPage reasoning integration** (`ChatPage.test.tsx`): mock WebSocket sends `reasoning_token` frames followed by `token` frames; verify `ThinkingBlock` renders, auto-collapses, persists after turn end.

### Integration

- E2E not required for this change (no new infrastructure, no DB schema changes).
