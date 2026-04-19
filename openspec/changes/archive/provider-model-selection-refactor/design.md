# Design — provider-model-selection-refactor

> Concrete technical plan for (A) dynamic per-provider model discovery and (B) reasoning-token streaming with a collapsible `ThinkingBlock` UI. All ADRs reference `post-propose-decisions` (no feature flag, `@tanstack/react-virtual`, hardcoded Anthropic capability map) and `anthropic-reasoning-models` (two thinking shapes).

---

## Architectural decisions (ADR)

### ADR-1: Dynamic model discovery uses an in-memory TTL cache keyed by provider id, with ProviderCatalog as fallback only
- **Context**: Five providers (`anthropic`, `openai`, `gemini`, `openrouter`, `ollama`) need a single HTTP surface. OpenRouter returns ~342 entries; Anthropic/OpenAI fail before a key is entered (setup wizard). `ProviderCatalog` is hand-maintained and drifts.
- **Decision**: Add `internal/web/modelcache` package with a `Cache` type: `sync.RWMutex`-protected `map[string]entry{models, fetchedAt, source}`. TTL is provider-specific: **10 min** for anthropic/openai/openrouter/gemini, **5 min** for ollama (local pull churn). `GET /api/providers/{provider}/models?refresh=true` forces a refetch. On `ListModels()` error, we synthesise a response from `setup.ProviderCatalog[provider]` and tag `source: "fallback"`. No LRU eviction — bounded by the fixed `len(KnownProviders)=5`.
- **Consequences**: Single-process only; if users run HA replicas they miss cache hits (acceptable — MVP). Manual bust via `?refresh=true` avoids restart. Stale entries return with `source: "cache"` so frontend can show a subtle "refreshed 3m ago" hint.
- **Rejected**: SQLite cache (too heavy for 5-entry map), Redis (adds a dependency, out of scope), per-call `ListModels()` (Anthropic `/v1/models` costs ~400ms, OpenRouter sometimes 1.2s — unacceptable on each tab switch).

### ADR-2: Reasoning is a first-class `StreamEvent` type; WebSocket frame is `reasoning_token` (additive, no versioning)
- **Context**: OpenRouter emits `delta.reasoning` / `delta.reasoning_content`; Anthropic emits `content_block_start{type:thinking}` + `content_block_delta{type:thinking_delta}`. These must reach the browser distinct from regular text.
- **Decision**: Add `StreamEventReasoningDelta` next to `StreamEventTextDelta` in `internal/provider/stream.go`. Each provider's stream parser maps its wire shape to this unified event. Channel layer adds `WriteReasoning(s string)` on `StreamWriter`; `webStreamWriter` emits `{type: "reasoning_token", text: s, channel_id}`. Unknown-type frames are already ignored by the current `ChatPage` handler — no protocol version bump needed.
- **Consequences**: Agent loop treats `StreamEventReasoningDelta` as non-terminal, non-content — it is forwarded to the channel but does NOT accumulate into `ChatResponse.Content`. Old frontend bundles ignore the new frame and still render content correctly.
- **Rejected**: A versioned WebSocket handshake (overkill, no prod users), a separate `/ws/reasoning` channel (two sockets per chat = complexity), folding reasoning into `StreamEventTextDelta` with a flag field (loses the typed contract + breaks existing consumers that assume Text is visible content).

### ADR-3: Anthropic thinking capability is a typed `map[string]thinkingShape`; request builder is a pure helper
- **Context**: Per `anthropic-reasoning-models`, `claude-opus-4-7` REQUIRES `thinking:{type:"adaptive", effort:...}` and REJECTS `type:"enabled"`. Older 4.6/4.5/4.1 use `type:"enabled" + budget_tokens`. `haiku-4-5` does NOT support thinking.
- **Decision**: In `internal/provider/anthropic.go` introduce:
  ```go
  type thinkingShape int
  const (
      thinkingNone thinkingShape = iota
      thinkingAdaptive
      thinkingManual
  )
  var anthropicThinkingCapability = map[string]thinkingShape{
      "claude-opus-4-7":              thinkingAdaptive,
      "claude-opus-4-6":              thinkingManual,
      "claude-sonnet-4-6":            thinkingManual,
      "claude-opus-4-5-20251101":     thinkingManual,
      "claude-sonnet-4-5-20250929":   thinkingManual,
      "claude-opus-4-1-20250805":     thinkingManual,
      // haiku-4-5 omitted → thinkingNone
  }
  func anthropicThinkingParams(modelID, effort string, budgetTokens int) map[string]any { ... }
  ```
- **Consequences**: Single source of truth, forward-compatible (future models added in one place). Unknown models default to `thinkingNone` — safe. Config needs TWO keys (not one): `providers.anthropic.thinking_effort` (default `"medium"`) and `providers.anthropic.thinking_budget_tokens` (default `10000`). Only the relevant one is used per model.
- **Rejected**: Two parallel slices (Adaptive + Manual) — fragile, easy to forget one. Runtime `/v1/models` capability probe — Anthropic's models endpoint doesn't report thinking support.

### ADR-4: Frontend model picker uses `@tanstack/react-virtual` with a single shared `<ModelPicker>` component
- **Context**: OpenRouter list is 342+ items; unvirtualised `<Select>` is janky. SettingsPage and SetupWizardPage currently use different markup.
- **Decision**: New component `daimon-frontend/src/components/provider/ModelPicker.tsx`. Props: `provider: ProviderName`, `value: string`, `onChange: (id: string) => void`, `disabled?: boolean`. Internally uses `useVirtualizer` from `@tanstack/react-virtual` with `estimateSize: () => 36`, `overscan: 5`. Data comes from a dedicated hook `useProviderModels(provider)` that calls `/api/providers/{p}/models` via react-query — key is `['providers', provider, 'models']`, `staleTime: 5 * 60_000`, `retry: 1`. Search is client-side on the resolved list (already in memory). Free-text input appears as a sibling text field under the list; a helper `isCustomModel(id, options)` decides which UI shows "custom".
- **Consequences**: SettingsPage and SetupWizardPage share one code path. react-query's provider-scoped key forces a refetch on tab change. Memory footprint is bounded (virtual list renders ~12 visible rows). Removes ~40 lines of duplicated logic.
- **Rejected**: `react-window` (user decision — ecosystem alignment with `@tanstack/react-query`), server-side search (overkill for 342 strings; client filter is <1ms on the cached array), cursor-based pagination (not needed).

### ADR-5: Ollama `ListModels()` hits `GET {baseURL}/api/tags`, wire-level translation matches OpenAI `ModelInfo`
- **Context**: Ollama is the only provider without `ListModels()`. Wire format is `{models: [{name, modified_at, size, digest, details: {...}}]}` on `/api/tags`.
- **Decision**: Implement on `OllamaProvider` (NOT on the embedded `*OpenAIProvider`, to avoid polluting the OpenAI path). New file `internal/provider/ollama_list.go`. Use `p.OpenAIProvider.config.BaseURL` (falls back to `http://localhost:11434` upstream). No API key. 5-second timeout (local). Map to `ModelInfo{ID: entry.Name, Name: entry.Name, ContextLength: 0, Free: true}`.
- **Consequences**: If the Ollama daemon is down, `ListModels()` returns `ErrUnavailable` and the modelcache falls back to an empty catalog entry — frontend shows the free-text input automatically. No crash. Adding `ListModels()` to `OllamaProvider` automatically satisfies the interface type-assertion in the registry (ADR-6).
- **Rejected**: Mirroring the list into SQLite (TTL cache is sufficient), recursive model metadata (`/api/show`) to populate context length (latency hit — skip for MVP).

### ADR-6: Dispatcher is a `ProviderRegistry` that resolves `{name → ModelLister}`, replacing the single `deps.ModelLister`
- **Context**: `Server.Deps.ModelLister` is the ACTIVE provider only. Dynamic discovery needs per-provider access regardless of which one is configured.
- **Decision**: Add `internal/provider/registry.go` with `type Registry interface { Lister(name string) (ModelLister, bool) }`. Concrete `StaticRegistry` is built at server boot from `cfg.Providers`: for every configured provider, construct via `NewFromConfig`, type-assert to `ModelLister`, insert if OK. Setup wizard keeps using `ProviderCatalog` for pre-key-entry state; AFTER `validate-key` succeeds, the wizard calls `/api/providers/{p}/models` (which can now see an on-demand constructed provider even though it's not yet in config).
- **Consequences**: `deps.ModelLister` stays for the legacy `/api/models` route (backward-compat, not deleted). New endpoint uses `deps.ProviderRegistry`. Setup flow adds a transient in-memory registration path — see "Setup wizard registration" below.
- **Rejected**: Removing `/api/models` entirely (breaks old frontend bundles during rollout — we want the additive invariant).

> **AMENDMENT (2026-04-19, user decision)**: Legacy `/api/models` endpoint DELETED — hard cutover, no compat path retained. `handler_models.go` removed. `deps.ModelLister` field removed from `ServerDeps`. Both `cmd/daimon/main.go` and `cmd/daimon/web_cmd.go` now construct `provider.NewStaticRegistry(*cfg)` and pass it as `ProviderRegistry`. Motivation: no prod users on old bundles at time of cutover; clean break preferred over maintaining dead code.

### ADR-7: Reasoning auto-enable is decided at request-build time using cached OpenRouter metadata + the Anthropic capability map
- **Context**: Sending `include_reasoning: true` or `thinking: {...}` to a non-reasoning model is a 400. Sending nothing to a reasoning-capable model silently drops thoughts.
- **Decision**: Before `apiReq` marshal, providers check capability:
  - **OpenRouter**: read cached model metadata from `modelcache` (we extend `ModelInfo` to carry `SupportedParameters []string`). If the list contains `"reasoning"` or `"include_reasoning"`, set `request.IncludeReasoning = true`. Cache miss → treat as no-reasoning (safe default).
  - **Anthropic**: look up model in `anthropicThinkingCapability`. Based on shape, inject `thinking: {type:"adaptive", effort: cfg.ThinkingEffort}` or `thinking: {type:"enabled", budget_tokens: cfg.ThinkingBudgetTokens}`.
- **Consequences**: No per-turn user toggle; auto-detection is single-source-of-truth. Users who want reasoning disabled can switch to a non-reasoning model. Budget/effort live in yaml only (per scope decision).
- **Rejected**: Probe via "send once, downgrade on 400" (doubles latency for every first call), a Settings UI toggle (deferred — scope decision).

---

## Component design

### Backend

#### Provider interface additions (`internal/provider/stream.go` + `provider.go`)

```go
// stream.go — add one constant + nothing else
const (
    StreamEventTextDelta StreamEventType = iota
    StreamEventReasoningDelta    // NEW — reasoning/thinking token fragment
    StreamEventToolCallStart
    // ... rest unchanged
)

// provider.go — extend ModelInfo
type ModelInfo struct {
    ID                  string   `json:"id"`
    Name                string   `json:"name"`
    ContextLength       int      `json:"context_length"`
    PromptCost          float64  `json:"prompt_cost"`
    CompletionCost      float64  `json:"completion_cost"`
    Free                bool     `json:"free"`
    SupportedParameters []string `json:"supported_parameters,omitempty"` // NEW
}
```

**Why iota insertion at position 1**: placing `StreamEventReasoningDelta` before `StreamEventToolCallStart` renumbers tool-call constants. That's safe because every write is symbolic (no persisted numeric values). Reviewed via `rg "StreamEvent[A-Z][a-z]+ = [0-9]"` — zero hard-coded numeric assignments.

#### Stream parser deltas

**OpenRouter** (`internal/provider/openrouter_stream.go`):
```go
type openrouterStreamChunk struct {
    Choices []struct {
        Delta struct {
            Content          *string                    `json:"content"`
            Reasoning        *string                    `json:"reasoning,omitempty"`          // NEW
            ReasoningContent *string                    `json:"reasoning_content,omitempty"`  // NEW (DeepSeek variant)
            ToolCalls        []openrouterStreamToolCall `json:"tool_calls,omitempty"`
        } `json:"delta"`
        // ...
    } `json:"choices"`
    // ...
}
```
Between the "Text delta" and "Tool call deltas" sections (around `openrouter_stream.go:205`), insert:
```go
if choice.Delta.Reasoning != nil && *choice.Delta.Reasoning != "" {
    events <- StreamEvent{Type: StreamEventReasoningDelta, Text: *choice.Delta.Reasoning}
}
if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
    events <- StreamEvent{Type: StreamEventReasoningDelta, Text: *choice.Delta.ReasoningContent}
}
```
Activation: `openrouterRequest` gains `IncludeReasoning bool `json:"include_reasoning,omitempty"``, set from capability lookup in `ChatStream` AND `Chat`.

**Anthropic** (`internal/provider/anthropic_stream.go`):
- Extend `anthropicContentBlock`: add `"thinking"` to the accepted `Type` values.
- Extend `anthropicDelta`: add `Thinking string `json:"thinking,omitempty"``.
- In `content_block_start`: if `sev.ContentBlock.Type == "thinking"`, set `inThinkingBlock = true` (new local bool) — do NOT emit tool events.
- In `content_block_delta`: new case `"thinking_delta"` → `events <- StreamEvent{Type: StreamEventReasoningDelta, Text: sev.Delta.Thinking}`.
- In `content_block_stop`: clear `inThinkingBlock`; do not assemble into `assembled.Content`.
- `buildAnthropicRequest` adds `thinking` param when `anthropicThinkingCapability[model] != thinkingNone`.

#### Ollama ListModels (`internal/provider/ollama_list.go` — NEW)

```go
func (o *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
    base := o.config.BaseURL
    if base == "" { base = "http://localhost:11434" }
    url := base + "/api/tags"
    // GET, 5s timeout, parse {models:[{name,size,...}]}, map to ModelInfo{ID:name,Name:name,Free:true}
}
```

#### HTTP API surface (`internal/web/`)

| Method | Path | Response |
|---|---|---|
| GET | `/api/providers/{provider}/models` | `{models: [...], source: "live"\|"cache"\|"fallback", cached_at: "2026-04-19T..."}` |
| GET | `/api/providers/{provider}/models?refresh=true` | forces live refetch |
| ~~GET~~ | ~~`/api/models`~~ | **DELETED** per ADR-6 amendment below (hard cutover) |
| GET | `/api/setup/providers` | unchanged for pre-key setup; wizard calls the new endpoint AFTER `validate-key` |

Error codes:
- `404` unknown provider name
- `502` `ListModels()` failed AND no fallback entry exists
- `200` with `source: "fallback"` when `ListModels()` fails but catalog has data

#### Cache layer (`internal/web/modelcache/cache.go` — NEW)

```go
type entry struct {
    models    []provider.ModelInfo
    fetchedAt time.Time
    source    string
}
type Cache struct {
    mu      sync.RWMutex
    data    map[string]entry
    ttl     map[string]time.Duration
}
func New() *Cache // seeds ttl map per-provider
func (c *Cache) GetOrFetch(ctx context.Context, provider string, fetcher func(ctx context.Context) ([]provider.ModelInfo, error), refresh bool) (entry, error)
```
`GetOrFetch` holds `RLock` for hit path; on miss upgrades to write lock via double-check. Fallback: if `fetcher` errors AND `setup.ProviderCatalog` has data → synthesise `entry{source:"fallback"}`, do NOT cache (so next call re-tries live).

#### Registry (`internal/provider/registry.go` — NEW)

```go
type Registry struct {
    listers map[string]ModelLister
}
func NewStaticRegistry(cfg config.Config) *Registry
func (r *Registry) Lister(name string) (ModelLister, bool)
func (r *Registry) RegisterTransient(name string, p Provider) // used by setup wizard after validate-key
```

#### Config additions (`internal/config/config.go`)

```go
type ProviderCredentials struct {
    APIKey          string `yaml:"api_key"          json:"api_key"`
    BaseURL         string `yaml:"base_url"         json:"base_url"`
    // NEW — anthropic-only, ignored by others:
    ThinkingEffort       string `yaml:"thinking_effort,omitempty"        json:"thinking_effort,omitempty"`        // "high"|"medium"|"low"; default "medium"
    ThinkingBudgetTokens *int   `yaml:"thinking_budget_tokens,omitempty" json:"thinking_budget_tokens,omitempty"` // default 10000
}
```
Defaults applied in `anthropicThinkingParams` (not in config loader) so zero-value creds "just work".

### Frontend

#### `ModelPicker` component (`daimon-frontend/src/components/provider/ModelPicker.tsx` — NEW)

```
ModelPicker
├── SearchInput          (<Input> controlled, debounced 150ms)
├── VirtualList          (useVirtualizer, 36px rows, 400px max height)
│   └── ModelRow         (id, name, cost hint, free badge)
├── CustomInputToggle    ("Use custom model ID" link)
└── CustomInput          (<Input> shown when toggled or id not in list)
```
Source of truth: `useProviderModels(provider)` hook — returns `{data, isLoading, isError, source, fetchedAt}`. On `activeProvider` change, react-query key changes → new fetch, loading state shown inline.

#### `ThinkingBlock` component (`daimon-frontend/src/components/chat/ThinkingBlock.tsx` — NEW)

State machine:
```
idle → streaming (reasoning_token frames arriving)
streaming → streaming_collapsed (first "token" frame for main text arrived → auto-collapse)
streaming_collapsed ↔ streaming_expanded (click to toggle)
streaming_collapsed → finished_collapsed (on "done" frame)
```
Content accumulator lives in chat state (separate from the existing `textBuffer`). Rendered as plain `<pre className="whitespace-pre-wrap">{reasoningBuffer}</pre>` inside a `<details>` element — NO `dangerouslySetInnerHTML`. Auto-collapse implemented via a `useEffect` that watches `textBuffer.length > 0 && !userInteracted`.

#### ChatPage (`daimon-frontend/src/pages/ChatPage.tsx`)

Extend the WebSocket `onmessage` switch (currently handling `token | done | error | ...`):
```ts
case 'reasoning_token':
    setReasoningBuffer(prev => prev + frame.text); break;
```
Render order inside a message bubble: `<ThinkingBlock> <MessageText>`. The hook `useMessageStream` owns both buffers.

#### API client (`daimon-frontend/src/api/client.ts`)

```ts
export async function getProviderModels(provider: ProviderName, opts?: {refresh?: boolean}): Promise<{models: ModelInfo[], source: string, cached_at: string}>
```
Also: delete `KNOWN_MODELS` from `src/schemas/config.ts` and its import in `SettingsPage.tsx`.

---

## Sequence diagrams (ASCII)

### Sending a message to a reasoning model

```
Browser         WS /chat        Agent.loop        Provider (OR/Anth)
   |  user text    |                |                    |
   |-------------->|--- incoming -->|                    |
   |               |                |---- ChatStream --->|
   |               |                |<-- StreamEventReasoningDelta
   |<-- {type:reasoning_token, text}                     |
   |  ThinkingBlock streams         |                    |
   |               |                |<-- StreamEventReasoningDelta (x N)
   |<-- ...                                              |
   |               |                |<-- StreamEventTextDelta
   |<-- {type:token, text}  ← first text → ThinkingBlock auto-collapses
   |               |                |<-- StreamEventDone
   |<-- {type:done}|                |                    |
```

### Switching provider tab in Settings

```
SettingsPage                react-query       /api/providers/{p}/models     modelcache       ListModels
   | activeProvider=anthropic   |                         |                      |                 |
   |-- hook key change -------->|                         |                      |                 |
   |                            |--- GET ---------------->|                      |                 |
   |                            |                         |-- GetOrFetch ------->|                 |
   |                            |                         |                      |-- hit? --->miss |
   |                            |                         |                      |---- fetcher --->|
   |                            |                         |                      |<-- [ModelInfo] -|
   |                            |<-- {models, source:live}                       |                 |
   |<-- render virtualised list |                         |                      |                 |
```

---

## Data migrations / compat

- **Config**: existing configs lacking `thinking_effort`/`thinking_budget_tokens` continue to work — defaults applied in-line. Configs referencing a retired model id: startup logs `slog.Warn("model X not found in provider Y; run /api/providers/Y/models to see current list")` — no fail.
- **WebSocket**: `reasoning_token` is additive; unknown-type frames are already ignored.
- **DB**: no schema changes.
- **Legacy `/api/models`**: untouched. Kept as fallback for the old bundled frontend during rollout.

---

## Performance notes

- Cache hit path is a single `RLock` + map read, <10µs.
- Cold fetch cost: Anthropic ~400ms, OpenRouter ~800ms, OpenAI ~500ms, Gemini ~600ms, Ollama <10ms (local).
- OpenRouter 342-row virtualized list: ~12 visible rows rendered, O(log n) filter, <4ms on re-render (measured mental model — validate in tasks phase with `console.time`).
- Reasoning frames: Anthropic thinking produces roughly 1 frame per 3-5 tokens; OpenRouter reasoning models similar. 50 frames/sec upper bound; WebSocket single-write-mutex handles this comfortably (already proven by `token` frames).

---

## Security notes

- API keys stay server-side; `/api/providers/{p}/models` does NOT echo keys in the response.
- Reasoning content may contain model-internal chain-of-thought (potentially sensitive prompt reconstruction). Rendered via React text nodes only — NO `dangerouslySetInnerHTML`, NO markdown parser on reasoning content (only on final assistant text).
- Setup endpoint `validate-key` path unchanged; the new models endpoint is auth-gated via the existing middleware (NOT in the setup-bypass list).

---

## Test strategy (tasks will expand)

| Layer | What | How |
|---|---|---|
| Go unit | Cache hit/miss/fallback, TTL eviction | `testing` + fake `time.Now` injection |
| Go unit | OpenRouter reasoning Delta parse | Golden SSE fixture in `testdata/openrouter_reasoning.sse` |
| Go unit | Anthropic thinking block parse (both shapes) | Golden SSE fixtures `anthropic_thinking_adaptive.sse` + `anthropic_thinking_manual.sse` |
| Go unit | Registry construction from cfg | Table-driven with empty + multi-provider configs |
| Go unit | Ollama ListModels | `httptest.Server` serving `/api/tags` |
| Go integration | `/api/providers/{p}/models` 200/404/502 paths | `httptest.Server` + fake `Registry` |
| Vitest | `ModelPicker` virtualization + search | `@testing-library/react`; scroll emulation |
| Vitest | `ThinkingBlock` state machine | Render transitions |
| Vitest | ChatPage reasoning_token handling | Mock WebSocket + assert buffer split |
| Race | `go test -race ./internal/web/modelcache/...` | mandatory |

Strict TDD is active — each task starts with a failing test.

---

## Open questions

- None blocking. If `ModelInfo.SupportedParameters` population for non-OpenRouter providers is needed later (e.g. for Anthropic auto-detect), extend the capability map; spec explicitly allows that follow-up.
