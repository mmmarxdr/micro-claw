# Provider Capability Specification

## Purpose

Defines the two new capability methods on the `Provider` interface, the static capability matrix for each provider, per-provider translation rules for `ContentBlock` to native wire format, and the graceful degradation contract when a provider cannot handle a block type.

## Requirements

### Requirement: Provider Interface Capability Methods

The `provider.Provider` interface SHALL declare two additional methods: `SupportsMultimodal() bool` and `SupportsAudio() bool`. Every concrete provider MUST implement both. `SupportsAudio()` implies `SupportsMultimodal()` — a provider MUST NOT return `SupportsAudio() == true` and `SupportsMultimodal() == false`.

#### Scenario: All providers implement both methods (compile-time)

- GIVEN every concrete type that implements `Provider`
- WHEN compiled
- THEN each satisfies the interface including `SupportsMultimodal()` and `SupportsAudio()`

---

### Requirement: Capability Matrix — Static Returns

Each provider SHALL return the following static values. Per-model granularity is deferred.

| Provider   | SupportsMultimodal | SupportsAudio |
|------------|--------------------|---------------|
| Anthropic  | true               | false         |
| OpenAI     | true               | true          |
| Gemini     | true               | true          |
| OpenRouter | true               | true          |
| Ollama     | false              | false         |

#### Scenario: Anthropic reports multimodal=true, audio=false

- GIVEN an Anthropic provider instance
- WHEN `SupportsMultimodal()` and `SupportsAudio()` are called
- THEN `SupportsMultimodal() == true` and `SupportsAudio() == false`

#### Scenario: Gemini reports both true

- GIVEN a Gemini provider instance
- WHEN both capability methods are called
- THEN both return `true`

#### Scenario: Ollama reports both false

- GIVEN an Ollama provider instance
- WHEN both capability methods are called
- THEN both return `false`

---

### Requirement: Fallback Provider Reports Logical AND

The `Fallback` provider, which wraps a pool of providers, SHALL return `SupportsMultimodal() == true` only when ALL members return `true`. It SHALL return `SupportsAudio() == true` only when ALL members return `true`. This is a safe under-statement.

#### Scenario: Fallback with mixed multimodal support

- GIVEN a Fallback pool containing one multimodal and one text-only provider
- WHEN `SupportsMultimodal()` is called on the Fallback
- THEN it returns `false`

---

### Requirement: Multimodal Provider Translates Image Blocks

When `SupportsMultimodal() == true`, the provider's request builder SHALL translate each `ContentBlock` with `Type == BlockImage` into the provider's native wire format. The translated form MUST include the base64-encoded bytes retrieved from the media store and the MIME type.

#### Scenario: Anthropic translates image block to native format

- GIVEN a `ChatMessage` containing a `BlockImage` block with a valid `MediaSHA256`
- WHEN the Anthropic provider builds a request
- THEN the wire message contains an Anthropic image object with `"type":"image"`, `"source":{"type":"base64","media_type":"<mime>","data":"<base64>"}`
- AND the text blocks are preserved in order

#### Scenario: OpenAI translates image block to image_url

- GIVEN a `ChatMessage` containing a `BlockImage` block
- WHEN the OpenAI provider builds a request
- THEN the wire content array contains `{"type":"image_url","image_url":{"url":"data:<mime>;base64,<b64>"}}`

#### Scenario: Gemini translates image block to inlineData

- GIVEN a `ChatMessage` containing a `BlockImage` block
- WHEN the Gemini provider builds a request
- THEN the wire parts array contains `{"inlineData":{"mimeType":"<mime>","data":"<b64>"}}`

---

### Requirement: Text-Only Provider Flattens Non-Text Blocks

When `SupportsMultimodal() == false`, the provider's request builder SHALL replace every non-text `ContentBlock` with a text placeholder of the form:

```
[<type> attached: <filename|type>, <human-readable size>, MIME <mime>, not processed by current model]
```

The original block SHALL NOT be forwarded to the LLM API.

#### Scenario: Ollama receives image block — flattened to placeholder

- GIVEN a `ChatMessage` with `Blocks{image block, text block "describe this"}`
- WHEN the Ollama provider builds a request
- THEN the wire content is a single text string containing `"[image attached:"` and `"not processed by current model"`
- AND the original `text` block content `"describe this"` is also present

---

### Requirement: Degradation Notice Appended to Reply

The agent loop SHALL detect when the most-recent user message contains media blocks AND `!provider.SupportsMultimodal()`. In that case, after receiving the assistant reply, it SHALL prepend a one-line user-facing notice to the reply content. The notice SHALL be selected based on which media type(s) are present:

- Image or document: `(I can't see images with the current model. I saved it for you.)`
- Audio: `(I can't listen to voice notes with the current model. I saved it for you.)`
- Mixed: image notice takes precedence.

The notice fires exactly once per turn. It is NOT generated if no media blocks were in the user message.

#### Scenario: Degradation notice for image on text-only provider

- GIVEN a user message with a `BlockImage` block and a text-only provider
- WHEN the agent loop processes the turn
- THEN the assistant reply is prefixed with `(I can't see images with the current model. I saved it for you.)`

#### Scenario: No notice when provider is multimodal

- GIVEN a user message with a `BlockImage` block and a multimodal-capable provider
- WHEN the agent loop processes the turn
- THEN the assistant reply does NOT contain the degradation notice prefix
