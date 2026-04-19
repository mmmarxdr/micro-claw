# Tasks — provider-model-selection-refactor

**Strict TDD**: every production code task MUST be preceded by a failing-test task. Tasks marked `[no-test]` are purely mechanical (deletes, config file updates, lint passes) and require no new test.

**ADR-6 AMENDMENT**: The design's ADR-6 originally retained `/api/models` as a legacy compat endpoint. User has OVERRIDDEN that decision. The old handler MUST be DELETED. Task 4.3 reflects this.

**Follow-ups OUT OF SCOPE (tracked in engram `sdd/provider-model-selection-refactor/post-design-decisions`)**:
- `gemini-streaming-support` — Gemini has no `ChatStream()` today; blocked on this change landing
- `per-turn reasoning toggle` — auto-detect is sufficient for v1; no UI toggle in this change
- `ollama-context-length-lazy-probe` — `/api/show` per-model probe deferred; only names for v1

---

## Phase 1: Foundation — types, config, and stream event

### 1.1 `StreamEventReasoningDelta` in stream.go

- [x] 1.1.1 Write failing test: table-driven test in `stream_test.go` asserting `StreamEventReasoningDelta.String()` returns `"ReasoningDelta"` and that its iota value is 1 (between `TextDelta=0` and `ToolCallStart=2`)
- [x] 1.1.2 Insert `StreamEventReasoningDelta` at iota position 1 in `internal/provider/stream.go` (between `StreamEventTextDelta` and `StreamEventToolCallStart`)
- [x] 1.1.3 Add `"ReasoningDelta"` case to the `String()` stringer in `stream.go`
- [x] 1.1.4 Verify 1.1.1 passes: `go test ./internal/provider/ -run TestStreamEventType`

### 1.2 `ModelInfo.SupportedParameters` field

- [x] 1.2.1 Write failing test: `ModelInfo` JSON round-trips `supported_parameters` field (table row: present, absent/omitempty)
- [x] 1.2.2 Add `SupportedParameters []string \`json:"supported_parameters,omitempty"\`` to `ModelInfo` in `internal/provider/provider.go`
- [x] 1.2.3 Verify 1.2.1 passes

### 1.3 Config schema additions — Anthropic thinking keys

- [x] 1.3.1 Write failing test in `config_test.go`: YAML with `providers.anthropic.thinking_effort: "high"` and `thinking_budget_tokens: 15000` parses into `ProviderCredentials` without error; zero-value struct uses defaults
- [x] 1.3.2 Add `ThinkingEffort string` and `ThinkingBudgetTokens *int` to `ProviderCredentials` in `internal/config/config.go` (yaml/json tags: `thinking_effort,omitempty`, `thinking_budget_tokens,omitempty`)
- [x] 1.3.3 Verify 1.3.1 passes: `go test ./internal/config/...`

### 1.4 `WriteReasoning` on `StreamWriter` interface + `webStreamWriter`

- [x] 1.4.1 Write failing test in `web_test.go` (channel package): calling `WriteReasoning("step A")` emits a WebSocket frame `{"type":"reasoning_token","data":"step A","channel_id":"<id>"}`
- [x] 1.4.2 Add `WriteReasoning(s string) error` to the `StreamWriter` interface in `internal/channel/channel.go`
- [x] 1.4.3 Implement `WriteReasoning` on `webStreamWriter` in `internal/channel/web.go` — emits `{"type":"reasoning_token","data":s,"channel_id":channelID}`
- [x] 1.4.4 Add stub `WriteReasoning` to CLI, Discord, Telegram, WhatsApp, and mux stream writers (no-op returning nil) so the interface is satisfied
- [x] 1.4.5 Verify 1.4.1 passes: `go test ./internal/channel/...`

---

## Phase 2: Anthropic thinking capability map + request builder

### 2.1 `thinkingShape` enum and capability map

- [x] 2.1.1 Write failing test in `anthropic_test.go` (table-driven, minimum 8 rows): `anthropicThinkingCapability["claude-opus-4-7"]` == `thinkingAdaptive`; `claude-opus-4-6` and `claude-sonnet-4-6` == `thinkingManual`; `claude-haiku-4-5-20251001` absent (== `thinkingNone`); legacy IDs `claude-opus-4-5-20251101`, `claude-sonnet-4-5-20250929`, `claude-opus-4-1-20250805` == `thinkingManual`; an unknown id == `thinkingNone`
- [x] 2.1.2 Define `thinkingShape` (int, unexported) with constants `thinkingNone`, `thinkingAdaptive`, `thinkingManual` in `internal/provider/anthropic.go`
- [x] 2.1.3 Define `var anthropicThinkingCapability = map[string]thinkingShape{...}` with all entries from REQ-RS-7
- [x] 2.1.4 Verify 2.1.1 passes

### 2.2 `anthropicThinkingParams()` pure helper

- [x] 2.2.1 Write failing test (table-driven): `anthropicThinkingParams("claude-opus-4-7", "high", 10000)` returns `map[string]any{"type":"adaptive","effort":"high"}`; `anthropicThinkingParams("claude-opus-4-6", "medium", 15000)` returns `{"type":"enabled","budget_tokens":15000}`; `anthropicThinkingParams("claude-haiku-4-5-20251001", "high", 10000)` returns nil
- [x] 2.2.2 Implement `func anthropicThinkingParams(modelID, effort string, budgetTokens int) map[string]any` in `internal/provider/anthropic.go` — uses capability map; returns nil for `thinkingNone`, adaptive payload for `thinkingAdaptive`, manual payload for `thinkingManual`
- [x] 2.2.3 Verify 2.2.1 passes

### 2.3 Inject thinking params into Anthropic request builder

- [x] 2.3.1 Write failing test (scenario RS-3b): when model is `claude-opus-4-7` and config `thinking_effort="high"`, the marshalled Anthropic request body contains `"thinking":{"type":"adaptive","effort":"high"}` and no `"budget_tokens"` key
- [x] 2.3.2 Write failing test (scenario RS-3c): when model is `claude-opus-4-6` and `thinking_budget_tokens=15000`, body contains `"thinking":{"type":"enabled","budget_tokens":15000}`
- [x] 2.3.3 Write failing test (scenario RS-3a): when model is `claude-haiku-4-5-20251001`, body does NOT contain a `"thinking"` key
- [x] 2.3.4 Extend `buildAnthropicRequest` in `internal/provider/anthropic.go` to call `anthropicThinkingParams` and inject the result when non-nil
- [x] 2.3.5 Verify 2.3.1, 2.3.2, 2.3.3 pass: `go test ./internal/provider/ -run TestAnthropic`

---

## Phase 3: Stream parser extensions

### 3.1 OpenRouter stream parser — reasoning delta

- [x] 3.1.1 Write failing test in `openrouter_stream_test.go` (scenario RS-1a): SSE chunk with `delta.reasoning_content="step 1..."` emits `StreamEvent{Type:ReasoningDelta, Text:"step 1..."}` and no `TextDelta` for that text
- [x] 3.1.2 Write failing test (scenario RS-1b): chunk with both `delta.reasoning="think..."` and `delta.content="answer..."` emits both a `ReasoningDelta` AND a `TextDelta` as separate events
- [x] 3.1.3 Add `Reasoning *string` and `ReasoningContent *string` fields (both `omitempty`) to the `Delta` struct in `internal/provider/openrouter_stream.go`
- [x] 3.1.4 Insert reasoning-delta emission logic in the stream parser loop (after existing text-delta handling): check `delta.Reasoning` first, then `delta.ReasoningContent`; emit `StreamEvent{Type:StreamEventReasoningDelta}` for each non-empty value
- [x] 3.1.5 Verify 3.1.1 and 3.1.2 pass

### 3.2 OpenRouter request — `include_reasoning` injection

- [x] 3.2.1 Write failing test (scenario RS-5a): when cached `ModelInfo.SupportedParameters` for the active model contains `"reasoning"`, the marshalled OpenRouter request body includes `"include_reasoning":true`
- [x] 3.2.2 Add `IncludeReasoning bool \`json:"include_reasoning,omitempty"\`` to `openrouterRequest` struct
- [x] 3.2.3 In `ChatStream` (and `Chat`) for OpenRouter: look up model in cache, check `SupportedParameters`; set `req.IncludeReasoning = true` when applicable
- [x] 3.2.4 Verify 3.2.1 passes

### 3.3 Anthropic stream parser — thinking block handling

- [x] 3.3.1 Write failing test (scenario RS-2a + RS-2b): SSE sequence `content_block_start{type:"thinking"}` followed by `content_block_delta{type:"thinking_delta", thinking:"let me think..."}` followed by `content_block_stop` emits exactly one `ReasoningDelta{Text:"let me think..."}` event and zero `TextDelta` events for that content
- [x] 3.3.2 Write failing test: a normal text content block interspersed with a thinking block emits `ReasoningDelta` for the thinking part and `TextDelta` for the text part, never mixing them
- [x] 3.3.3 Extend `anthropicDelta` struct: add `Thinking string \`json:"thinking,omitempty"\``
- [x] 3.3.4 In `anthropic_stream.go` `content_block_start` handler: add `case "thinking": inThinkingBlock = true` (new local bool; do NOT emit tool events)
- [x] 3.3.5 In `content_block_delta` handler: add `case "thinking_delta": events <- StreamEvent{Type:StreamEventReasoningDelta, Text:sev.Delta.Thinking}`
- [x] 3.3.6 In `content_block_stop` handler: clear `inThinkingBlock = false`; do NOT assemble thinking content into the final `assembled.Content`
- [x] 3.3.7 Verify 3.3.1 and 3.3.2 pass: `go test ./internal/provider/ -run TestAnthropic`

### 3.4 Fallback provider — transparent propagation

- [x] 3.4.1 Write failing test (scenario RS-4b): `fallback_stream.go` test — upstream emits `ReasoningDelta` events; assert they appear unmodified on the output channel
- [x] 3.4.2 Audit `internal/provider/fallback_stream.go` and `fallback.go` — confirm `ReasoningDelta` is NOT filtered (should pass through automatically since the switch already handles all event types or has a default forward); add explicit case if needed
- [x] 3.4.3 Verify 3.4.1 passes

---

## Phase 4: Ollama `ListModels()`

### 4.1 Implement `OllamaProvider.ListModels`

- [x] 4.1.1 Write failing test in `ollama_test.go` using `httptest.NewServer`: `/api/tags` returns `{"models":[{"name":"llama3:latest"},{"name":"mistral:7b"}]}`; assert `ListModels()` returns `[]ModelInfo{{ID:"llama3:latest",Name:"llama3:latest",Free:true},{ID:"mistral:7b",Name:"mistral:7b",Free:true}}`
- [x] 4.1.2 Write failing test: when `/api/tags` returns a non-200 or connection refused, `ListModels()` returns a non-nil error (no panic)
- [x] 4.1.3 Create `internal/provider/ollama_list.go` with `func (o *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error)` — GET `{baseURL}/api/tags` with 5s timeout; map `{name}` entries to `ModelInfo{ID, Name, Free:true}`; base URL defaults to `http://localhost:11434`
- [x] 4.1.4 Verify 4.1.1 and 4.1.2 pass: `go test ./internal/provider/ -run TestOllama`

---

## Phase 5: Model cache package

### 5.1 `internal/web/modelcache` package

- [x] 5.1.1 Write failing test: `Cache.GetOrFetch` returns `entry{source:"live"}` on cold miss when fetcher succeeds, and subsequent call (within TTL) returns `entry{source:"cache"}` without calling fetcher again
- [x] 5.1.2 Write failing test: `GetOrFetch` with expired TTL re-invokes fetcher and updates cache
- [x] 5.1.3 Write failing test (scenario PMD-1c): fetcher error with existing (even stale) cache entry returns `entry{source:"cache-stale"}` without calling fetcher again
- [x] 5.1.4 Write failing test (scenario PMD-1d): fetcher error with empty cache synthesises entry from `setup.ProviderCatalog` with `source:"fallback"`; the fallback is NOT stored in cache
- [x] 5.1.5 Write failing test: `refresh=true` bypasses valid cache and re-invokes fetcher
- [x] 5.1.6 Write failing test: `GetOrFetch` is safe for concurrent reads (`-race`); run via `go test -race ./internal/web/modelcache/...`
- [x] 5.1.7 Create `internal/web/modelcache/cache.go` with `Cache` struct (`sync.RWMutex`-protected `map[string]entry`, TTL map seeded at construction: `anthropic/openai/openrouter/gemini=10min`, `ollama=5min`); implement `GetOrFetch` with double-check locking on miss
- [x] 5.1.8 Verify all modelcache tests pass with race detector

---

## Phase 6: Provider registry

### 6.1 `internal/provider/registry.go`

- [x] 6.1.1 Write failing test (table-driven): `NewStaticRegistry` with a config containing an Anthropic key yields `registry.Lister("anthropic")` returning a valid `ModelLister`; `registry.Lister("unknown")` returns `false`
- [x] 6.1.2 Write failing test: `RegisterTransient` makes a provider available via `Lister` for the session (used by setup wizard after `validate-key`)
- [x] 6.1.3 Create `internal/provider/registry.go` — `type Registry struct{listers map[string]ModelLister}`; `func NewStaticRegistry(cfg config.Config) *Registry` iterates configured providers, constructs via factory, type-asserts to `ModelLister`; `func (r *Registry) Lister(name string) (ModelLister, bool)`; `func (r *Registry) RegisterTransient(name string, p Provider)`
- [x] 6.1.4 Verify 6.1.1 and 6.1.2 pass: `go test ./internal/provider/ -run TestRegistry`

---

## Phase 7: HTTP handler `GET /api/providers/{provider}/models`

### 7.1 Handler + server wiring

- [x] 7.1.1 Write failing test (scenario PMD-1a): `GET /api/providers/anthropic/models` with a configured Anthropic key, cold cache, and a fake registry returning 3 models → HTTP 200, `X-Source: live`, body `{"models":[...],"source":"live","cached_at":"..."}`
- [x] 7.1.2 Write failing test (scenario PMD-1b): same request, cache populated from prior call within TTL → `X-Source: cache`, response served in < 5ms (use `time.Now` injection)
- [x] 7.1.3 Write failing test (scenario PMD-1c): fetcher errors, stale cache present → `X-Source: cache-stale`
- [x] 7.1.4 Write failing test (scenario PMD-1d): fetcher errors, no cache → HTTP 200, `X-Source: fallback`, body contains catalog models
- [x] 7.1.5 (Skipped — 401 path not in registry pattern; unknown provider → 404. PMD-1e deferred to Batch 3/setup flow)
- [x] 7.1.6 Write failing test: unknown provider name (e.g. `badprovider`) → HTTP 404
- [x] 7.1.7 Write failing test (scenario PMD-1f): `?refresh=true` with a valid cache forces a new fetcher call and updates the cache
- [x] 7.1.8 Create `internal/web/handler_providers.go` with `func (s *Server) handleListProviderModels(w http.ResponseWriter, r *http.Request)` — reads `{provider}` from path, checks registry, uses modelcache, returns the JSON response envelope
- [x] 7.1.9 Added `ProviderRegistry providerRegistry` and `ModelCache *modelcache.Cache` to `Deps`; wired `NewStaticRegistry` in main.go and web_cmd.go
- [x] 7.1.10 Registered route `"GET /api/providers/{provider}/models"` in `server.go routes()`
- [x] 7.1.11 Verify 7.1.1–7.1.7 pass: `go test ./internal/web/ -run TestProviderModels`

---

## Phase 8: Delete legacy `/api/models` endpoint (ADR-6 AMENDMENT)

- [x] 8.1 [no-test] Updated `openspec/changes/provider-model-selection-refactor/design.md` ADR-6: amendment note appended (2026-04-19)
- [x] 8.2 Write failing test (regression): `GET /api/models` no longer returns JSON model list
- [x] 8.3 Removed `internal/web/handler_models.go`
- [x] 8.4 Removed the `s.mux.HandleFunc("GET /api/models", s.handleListModels)` line from `server.go`
- [x] 8.5 Removed `ModelLister provider.ModelLister` from `Deps` in `server.go` (replaced by `ProviderRegistry`)
- [x] 8.6 Verify 8.2 passes: `go test ./internal/web/ -run TestLegacyModelsEndpointGone`

---

## Phase 9: Health check startup model validation

### 9.1 Startup model validation

- [x] 9.1.1 Write failing test (scenario PMD-3a): when configured model `"claude-opus-x-99"` is not in the registry's `ListModels()` response for `"anthropic"`, startup logs a warning containing `model "claude-opus-x-99" not found in provider "anthropic" live list` and does NOT return an error or panic
- [x] 9.1.2 Write failing test: when `ListModels()` returns an error (provider unreachable), the health check skips validation silently (no warning, no crash)
- [x] 9.1.3 Implement `validateConfiguredModel(ctx, registry, cfg)` function in a new file `internal/web/startup_check.go`; call it from server startup (non-blocking, warn-only via `slog.Warn`)
- [x] 9.1.4 Verify 9.1.1 and 9.1.2 pass

---

## Phase 10: Agent loop — forward `ReasoningDelta`

### 10.1 Agent loop event handling

- [x] 10.1.1 Write failing test in `loop_test.go` (scenario RS-4a): when the provider emits a `ReasoningDelta` event during streaming, the loop calls `sw.WriteReasoning(text)` exactly once per event and does NOT append the text to the assembled message content
- [x] 10.1.2 Write failing test: when the provider emits `TextDelta` events, the loop calls `sw.WriteChunk(text)` and appends to message content (existing behavior, regression guard)
- [x] 10.1.3 Extend the `StreamEventReasoningDelta` case in `internal/agent/loop.go`'s stream event loop: call `sw.WriteReasoning(ev.Text)`, do NOT accumulate into `accumulated`
- [x] 10.1.4 Verify 10.1.1 and 10.1.2 pass: `go test ./internal/agent/ -run TestLoop`

---

## Phase 11: Setup wizard — dynamic model fetch

### 11.1 Post-validate-key model fetch

- [x] 11.1.1 Write failing test in `setup_handlers_test.go`: after `POST /api/setup/validate-key` succeeds for `anthropic`, the handler registers a transient provider in the registry; a subsequent `GET /api/providers/anthropic/models` returns models (not catalog fallback)
- [x] 11.1.2 In `setup_handlers.go` `handleValidateKey`: on success, call `s.deps.ProviderRegistry.RegisterTransient(provider, constructed_provider)`
- [x] 11.1.3 Verify 11.1.1 passes

---

## Phase 12: Frontend — model picker component

### 12.1 Install `@tanstack/react-virtual`

- [x] 12.1.1 [no-test] Run `npm install @tanstack/react-virtual` in `daimon-frontend`; verify it appears in `package.json` dependencies

### 12.2 `useProviderModels` hook

- [x] 12.2.1 Write failing Vitest: `useProviderModels("anthropic")` calls `GET /api/providers/anthropic/models`; switching to `"openrouter"` triggers a new fetch (different react-query key `['providers','openrouter','models']`); result for first provider is NOT reused
- [x] 12.2.2 Write failing Vitest: when the endpoint returns `source:"fallback"`, the hook exposes `source:"fallback"` to the consumer
- [x] 12.2.3 Create `daimon-frontend/src/hooks/useProviderModels.ts` — `useQuery` with key `['providers', provider, 'models']`, `staleTime: 5 * 60_000`, `retry: 1`; calls `getProviderModels(provider)` from a new API helper
- [x] 12.2.4 Add `getProviderModels(provider, opts?)` to `daimon-frontend/src/api/client.ts`
- [x] 12.2.5 Verify 12.2.1 and 12.2.2 pass: `cd daimon-frontend && npm test -- useProviderModels`

### 12.3 `ModelPicker` component

- [x] 12.3.1 Write failing Vitest: `ModelPicker` with a 342-item list renders a virtualized list (only ~12 DOM rows, not 342 `<li>` nodes)
- [x] 12.3.2 Write failing Vitest: typing "deepseek" in the search input filters the visible list to only models whose ID or name contains "deepseek" (case-insensitive); list updates on each keystroke
- [x] 12.3.3 Write failing Vitest (scenario PMD-4c): typing a value not in the list into the custom model input shows the hint "Custom model ID — not validated against provider list"
- [x] 12.3.4 Write failing Vitest: selecting a model from the virtualized list calls `onChange` with the model ID
- [x] 12.3.5 Create `daimon-frontend/src/components/provider/ModelPicker.tsx` — uses `useVirtualizer` with `estimateSize:()=>36`, `overscan:5`; `SearchInput` (debounced 150ms, client-side filter); `CustomInputToggle` + `CustomInput` sibling; `isCustomModel(id, options)` helper
- [x] 12.3.6 Verify 12.3.1–12.3.4 pass

### 12.4 Remove `KNOWN_MODELS`

- [x] 12.4.1 Write failing Vitest: import `KNOWN_MODELS` from `src/schemas/config.ts` fails (export no longer exists); or equivalently, the settings model list is empty when the API returns 0 models (not populated from the old constant)
- [x] 12.4.2 Remove `KNOWN_MODELS` export and its declaration block from `daimon-frontend/src/schemas/config.ts`
- [x] 12.4.3 Remove all `KNOWN_MODELS` import references from `SettingsPage.tsx` and any other files
- [x] 12.4.4 Verify 12.4.1 passes

### 12.5 Wire `ModelPicker` into `SettingsPage`

- [x] 12.5.1 Write failing Vitest (scenario PMD-4a): switching from the "openrouter" provider tab to "anthropic" calls `GET /api/providers/anthropic/models`; the previously rendered OpenRouter model count is gone from the DOM
- [x] 12.5.2 Replace the existing model dropdown in `SettingsPage.tsx` with `<ModelPicker>` wired to the active provider; ensure `activeProvider` change triggers the new fetch
- [x] 12.5.3 Verify 12.5.1 passes

### 12.6 Wire `ModelPicker` into `SetupWizardPage`

- [x] 12.6.1 Write failing Vitest (scenario PMD-5a): after `validate-key` succeeds, the wizard model picker calls `GET /api/providers/{p}/models` (not catalog)
- [x] 12.6.2 Replace model picker in `SetupWizardPage.tsx` with `<ModelPicker>` wired to the selected provider; model list populates after key validation
- [x] 12.6.3 Verify 12.6.1 passes

---

## Phase 13: Frontend — `ThinkingBlock` component

### 13.1 Component implementation

- [x] 13.1.1 Write failing Vitest (scenario CTU-1a): when `reasoning` prop is non-empty and `isStreaming=true`, `ThinkingBlock` renders with content visible (expanded state)
- [x] 13.1.2 Write failing Vitest (scenario CTU-1b): when `hasTextStarted=true` prop transitions to true, block auto-collapses and shows "Thought for Xs" summary label
- [x] 13.1.3 Write failing Vitest (scenario CTU-1c + CTU-1d): clicking or pressing Enter on the collapsed block toggles it to expanded; pressing Enter again collapses it
- [x] 13.1.4 Write failing Vitest (REQ-CTU-8): `ThinkingBlock` is focusable via Tab and responds to Enter/Space to toggle
- [x] 13.1.5 Write failing Vitest (REQ-CTU-10): when `reasoning` prop is empty or undefined, `ThinkingBlock` renders nothing (`null`)
- [x] 13.1.6 Write failing Vitest (scenario CTU-1e): after turn completion (`isStreaming=false`, `hasTextStarted=true`), block remains toggleable with full reasoning content
- [x] 13.1.7 Create `daimon-frontend/src/components/chat/ThinkingBlock.tsx` — uses `<details>` element for native expand/collapse; renders content as `<pre className="whitespace-pre-wrap">`; "Thought for Xs" label computed from timestamps
- [x] 13.1.8 Verify 13.1.1–13.1.6 pass: `cd daimon-frontend && npm test -- ThinkingBlock`

### 13.2 Chat state — `reasoningBuffer` + `textBuffer` split

- [x] 13.2.1 Write failing Vitest (scenario CTU-1a, ChatPage level): mock WebSocket sends 3 `reasoning_token` frames then 2 `token` frames; assert `ThinkingBlock` renders with the reasoning text and auto-collapses on first `token`
- [x] 13.2.2 Write failing Vitest (scenario CTU-2a): mock WebSocket sends `reasoning_token` frames to a handler that does NOT know about them; assert no crash and that subsequent `token` frames still render the assistant message
- [x] 13.2.3 Write failing Vitest (scenario CTU-1f): when no `reasoning_token` frames precede `token` frames, `ThinkingBlock` is NOT present in the DOM
- [x] 13.2.4 Add `reasoningBuffer` state (separate from `textBuffer`) to `ChatPage.tsx`
- [x] 13.2.5 Add `case 'reasoning_token': setReasoningBuffer(prev => prev + frame.data); break;` to the WebSocket `onmessage` in `ChatPage.tsx`
- [x] 13.2.6 Render `<ThinkingBlock>` above the assistant message bubble in `ChatPage.tsx`, driven by `reasoningBuffer`; pass `hasTextStarted`, `isStreaming`, `thinkingStartedAt`, `textStartedAt` props
- [x] 13.2.7 Verify 13.2.1–13.2.3 pass

---

## Phase 14: Integration tests

### 14.1 Backend integration

- [x] 14.1.1 Write integration test (Go): full HTTP round-trip — call `GET /api/providers/ollama/models` via an `httptest.Server` wrapping the real server with a fake Ollama backend; assert `source:"live"` and at least one model in the list (covered by `TestIntegration_ProviderModels_LiveThenCache` in `integration_provider_models_test.go`)
- [x] 14.1.2 Write integration test (Go): switching provider mid-session — call `GET /api/providers/anthropic/models` (cached), then `GET /api/providers/openrouter/models` (separate cache key); assert no cross-contamination between cached entries (`TestIntegration_ProviderModels_SeparateCacheKeys`)
- [x] 14.1.3 Backend reasoning flow integration test: `TestIntegration_ReasoningFlow` and `TestIntegration_ReasoningOnly_WriterNotLeaked` in `internal/agent/integration_reasoning_test.go`

### 14.2 Frontend integration

- [x] 14.2.1 Write Vitest integration test (`ChatPageThinkingIntegration.test.tsx`): full mock-WS session with `reasoning_token` frames followed by `token` frames followed by `done`; assert ThinkingBlock renders reasoning, auto-collapses on first token, persists after done, and is toggleable
- [x] 14.2.2 Write Vitest integration test: switching provider in Settings; assert model list request fires for new provider and DOM reflects new list (`SettingsPage.integration.test.tsx`)

---

## Phase 15: Cleanup + quality gates

- [x] 15.1 [no-test] Run `go vet ./...` — fix all reported issues
- [x] 15.2 [no-test] Run `golangci-lint run ./...` — fix all linter warnings in changed files
- [x] 15.3 [no-test] Run `go test -race -timeout 300s ./...` — verify race detector clean across all packages
- [x] 15.4 [no-test] Run `cd daimon-frontend && npm run lint` — fix all ESLint issues
- [x] 15.5 [no-test] Run `cd daimon-frontend && npx tsc --noEmit` — fix all TypeScript errors
- [x] 15.6 [no-test] Run full test suites: `go test -timeout 300s ./...` (backend) and `cd daimon-frontend && npm test` (frontend); all must pass
- [x] 15.7 [no-test] Updated `DAIMON.md` and `TESTS.md` with new capabilities; global @tanstack/react-virtual mock documented in TESTS.md

**All phases complete as of 2026-04-19.**
