# Content Types Specification

## Purpose

Defines the `internal/content` package: the `ContentBlock` discriminated union, the `Blocks` slice helper, JSON marshal/unmarshal shims for backward compatibility, and the types that replace `string` on `IncomingMessage`, `OutgoingMessage`, and `ChatMessage`.

## Requirements

### Requirement: BlockType Constants

The `content` package SHALL define exactly four `BlockType` constants: `BlockText`, `BlockImage`, `BlockAudio`, `BlockDocument`. No other values are part of the core type contract.

#### Scenario: All four constants exist and are distinct

- GIVEN the `content` package
- WHEN `BlockText`, `BlockImage`, `BlockAudio`, `BlockDocument` are compared pairwise
- THEN all four values are distinct strings

---

### Requirement: ContentBlock Field Invariants

A `ContentBlock` MUST satisfy exactly one of two field invariants at all times:
- `Type == BlockText` → `Text` is non-empty; `MediaSHA256`, `MIME`, `Size` are zero-valued.
- `Type != BlockText` → `MediaSHA256` is a lowercase hex SHA256 string; `MIME` is set; `Text` is empty.

`Filename` is optional in both cases.

#### Scenario: Text block round-trip

- GIVEN a `ContentBlock{Type: BlockText, Text: "hello"}`
- WHEN marshalled to JSON and back
- THEN `Type == BlockText`, `Text == "hello"`, `MediaSHA256 == ""`

#### Scenario: Image block round-trip

- GIVEN a `ContentBlock{Type: BlockImage, MediaSHA256: "<sha256>", MIME: "image/jpeg", Size: 1024}`
- WHEN marshalled to JSON and back
- THEN `Type == BlockImage`, `MediaSHA256 == "<sha256>"`, `MIME == "image/jpeg"`, `Text == ""`

---

### Requirement: IncomingMessage Content Field

`IncomingMessage` SHALL carry `Content content.Blocks` replacing the former `Text string` field. It SHALL expose a `Text() string` method returning `Content.TextOnly()` for backward-compatible call sites.

#### Scenario: IncomingMessage with text only

- GIVEN an `IncomingMessage` with `Content = content.TextBlock("hello")`
- WHEN `msg.Text()` is called
- THEN it returns `"hello"`

#### Scenario: IncomingMessage with mixed content

- GIVEN an `IncomingMessage` with `Content = Blocks{image block, text block "caption"}`
- WHEN `msg.Text()` is called
- THEN it returns `"caption"` (image block is silently skipped)

---

### Requirement: OutgoingMessage Remains Text-Only

`OutgoingMessage` SHALL retain its `Text string` field unchanged for this change. The type SHALL NOT carry `Content content.Blocks`. This deferral is documented as explicit design decision D3 (outgoing multimodal deferred).

#### Scenario: OutgoingMessage has no Content field

- GIVEN the `OutgoingMessage` type definition
- WHEN compiled
- THEN it has a `Text string` field and no `Content` field

---

### Requirement: ChatMessage Content Field

`provider.ChatMessage` SHALL carry `Content content.Blocks` replacing the former `Content string` field. It SHALL implement a custom `UnmarshalJSON` that accepts both legacy string form and modern array form. It SHALL NOT implement a custom `MarshalJSON`; default struct marshalling MUST emit the new array form.

#### Scenario: ChatMessage UnmarshalJSON accepts legacy string

- GIVEN a JSON object `{"role":"user","content":"hello"}`
- WHEN unmarshalled into `ChatMessage`
- THEN `Role == "user"`, `Content == Blocks{{Type: BlockText, Text: "hello"}}`

#### Scenario: ChatMessage UnmarshalJSON accepts modern array

- GIVEN a JSON object `{"role":"user","content":[{"type":"text","text":"hello"}]}`
- WHEN unmarshalled into `ChatMessage`
- THEN `Role == "user"`, `Content == Blocks{{Type: BlockText, Text: "hello"}}`

#### Scenario: ChatMessage MarshalJSON always writes array

- GIVEN a `ChatMessage{Role: "user", Content: content.TextBlock("hello")}`
- WHEN marshalled to JSON
- THEN the `content` field is a JSON array, not a string
- AND the value is `[{"type":"text","text":"hello"}]`

---

### Requirement: Blocks.TextOnly Helper

`Blocks.TextOnly()` SHALL return all text blocks concatenated with newlines, in order. Non-text blocks SHALL be silently skipped. An all-media slice SHALL return `""`.

#### Scenario: TextOnly concatenates multiple text blocks

- GIVEN `Blocks{{Type: BlockText, Text: "foo"}, {Type: BlockImage, ...}, {Type: BlockText, Text: "bar"}}`
- WHEN `TextOnly()` is called
- THEN the result is `"foo\nbar"`

#### Scenario: TextOnly on empty slice

- GIVEN `Blocks{}`
- WHEN `TextOnly()` is called
- THEN the result is `""`

---

### Requirement: UnmarshalBlocks Shared Shim

`content.UnmarshalBlocks(raw json.RawMessage)` SHALL be a package-level function that normalizes both legacy string and modern array JSON into a `Blocks` slice. `nil`/`null`/empty input SHALL return `(nil, nil)`.

#### Scenario: Legacy string input

- GIVEN `json.RawMessage(`"hello"`)` 
- WHEN `UnmarshalBlocks` is called
- THEN result is `Blocks{{Type: BlockText, Text: "hello"}}`, err is nil

#### Scenario: Modern array input

- GIVEN `json.RawMessage(`[{"type":"text","text":"hi"}]`)`
- WHEN `UnmarshalBlocks` is called
- THEN result is `Blocks{{Type: BlockText, Text: "hi"}}`, err is nil

#### Scenario: Null input

- GIVEN `json.RawMessage("null")`
- WHEN `UnmarshalBlocks` is called
- THEN result is nil, err is nil
