# Spec — provider-model-discovery

Dynamic per-provider model listing with live API fetch, in-memory TTL cache, and static catalog fallback.

## Requirements

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

## Scenarios

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
