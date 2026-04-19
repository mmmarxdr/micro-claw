# Spec — reasoning-stream

Transport-level abstraction for reasoning tokens across providers. Covers stream event types, Delta-struct extensions for OpenRouter and Anthropic, auto-activation rules, and provider configuration.

## Requirements

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

## Scenarios

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
