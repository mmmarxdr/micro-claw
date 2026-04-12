# Agent Loop Multimodal Specification

## Purpose

Defines how the agent loop integrates `IncomingMessage.Content` into conversation history, performs the single degradation check, and how `context.go` iterates content blocks for context building and token accounting.

## Requirements

### Requirement: IncomingMessage Content Appended to Conversation

The agent loop's `processMessage` SHALL append the incoming message to `conv.Messages` as a `ChatMessage` with `Role: "user"` and `Content: msg.Content` (the full `content.Blocks` slice, not just text). The former `Content: msg.Text` assignment MUST be replaced.

#### Scenario: Multimodal message appended with all blocks

- GIVEN an `IncomingMessage` with `Content = [BlockText, BlockImage]`
- WHEN `processMessage` runs
- THEN `conv.Messages` gains a new entry with `Role == "user"` and `Content` containing both blocks

#### Scenario: Text-only message appended unchanged

- GIVEN an `IncomingMessage` with `Content = [BlockText{"hello"}]`
- WHEN `processMessage` runs
- THEN `conv.Messages` gains a new entry with `Content.TextOnly() == "hello"`

---

### Requirement: Degradation Check Before Provider Call

The agent loop SHALL perform exactly one degradation check per user turn, immediately before calling `provider.Chat()`. The check evaluates the most-recently appended `ChatMessage` (the current user turn only). The loop SHALL NOT walk prior turns.

If `!provider.SupportsMultimodal()` AND the current user message `Content.HasMedia() == true`, the loop SHALL set a `degraded bool` flag. After the provider returns, if `degraded == true`, the loop SHALL prepend the appropriate notice to the reply content.

#### Scenario: Multimodal message on text-only provider sets degraded flag

- GIVEN a user message with a `BlockImage` block
- AND the provider returns `SupportsMultimodal() == false`
- WHEN the agent loop processes the turn
- THEN the assistant reply is prefixed with `(I can't see images with the current model. I saved it for you.)`

#### Scenario: Multimodal message on capable provider — no degradation

- GIVEN a user message with a `BlockImage` block
- AND the provider returns `SupportsMultimodal() == true`
- WHEN the agent loop processes the turn
- THEN the assistant reply does NOT start with a degradation notice

#### Scenario: Text-only message — no degradation regardless of provider

- GIVEN a user message with only `BlockText` blocks
- AND the provider returns `SupportsMultimodal() == false`
- WHEN the agent loop processes the turn
- THEN `degraded == false` and no notice is prepended

---

### Requirement: Message With Only Non-Text Blocks on Text-Only Provider

When a user message contains no text blocks at all and the provider is text-only, the degradation path still fires. The LLM receives only the placeholder text from the flattened non-text blocks. The reply MUST include the degradation notice.

#### Scenario: Image-only message on text-only provider

- GIVEN a user message with `Content = [BlockImage{...}]`
- AND the provider is text-only
- WHEN the agent loop processes the turn
- THEN the provider receives a flattened placeholder: `"[image attached: ...]"`
- AND the reply is prefixed with the degradation notice

---

### Requirement: context.go Iterates Blocks for Token Counting and History

`context.go`'s context builder SHALL iterate `ChatMessage.Content` block by block. For token counting and summarization, non-text blocks SHALL use a per-provider constant estimate (Anthropic: ~1500 tokens/image; OpenAI: 85+tiles; Gemini: 258). For history truncation (oldest-first drop), the unit of truncation is a whole `ChatMessage`, never individual blocks within a message.

#### Scenario: Context builder counts token estimate for image block

- GIVEN a conversation history containing a `ChatMessage` with one `BlockImage`
- WHEN the context builder estimates the token cost of that message for an Anthropic provider
- THEN the image block contributes approximately 1500 tokens to the estimate

#### Scenario: History truncation drops whole messages, not individual blocks

- GIVEN a context window that is over budget with three messages, the oldest containing mixed blocks
- WHEN the context builder truncates
- THEN the entire oldest `ChatMessage` is dropped
- AND no individual block from that message is retained

---

### Requirement: Degradation Logging

When the degradation path fires, the agent loop SHALL emit a structured log at INFO level with at minimum: `"degradation"`, `"provider_name"`, and `"block_types"` (list of the non-text types that triggered degradation).

#### Scenario: Degradation log emitted

- GIVEN a text-only provider receiving an image block
- WHEN degradation fires
- THEN a log entry at INFO level with key `"degradation"` is emitted
