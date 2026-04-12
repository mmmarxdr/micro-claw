# Forward-Compat Reader Specification

## Purpose

Defines the rollback-safety constraint introduced by design Risk 1 (Decision 5): a reader that allows the OLD binary (pre-Phase-1) to load conversations written in the NEW JSON shape without crashing or losing content. This reader MUST be shipped and deployed BEFORE any phase writes the new array form to disk.

## Requirements

### Requirement: Forward-Compat Reader Ships Before New Writers

The forward-compat reader — the `UnmarshalJSON` shim on `ChatMessage` that accepts both `"content":"string"` and `"content":[...]` — SHALL be deployed to production on the main branch BEFORE any commit that causes any code path to serialize `ChatMessage.Content` as a JSON array. No phase that writes the new shape MAY merge until this requirement is satisfied.

#### Scenario: Old binary reads legacy string content (baseline)

- GIVEN a conversation stored with `"content":"hello"` (pre-multimodal form)
- WHEN any binary version — old or new — loads it
- THEN `ChatMessage.Content` is `Blocks{{Type: BlockText, Text: "hello"}}` or equivalent text
- AND no error is returned

#### Scenario: Old binary reads new array content — flattens to text

- GIVEN a conversation stored with `"content":[{"type":"text","text":"hello"}]` (new form)
- AND the reading binary has the forward-compat shim installed
- WHEN the conversation is loaded
- THEN text content is preserved: `Content.TextOnly() == "hello"`
- AND no error is returned

---

### Requirement: Array With Text Blocks Only — Joined

When the array contains only text blocks, `TextOnly()` SHALL join them in order with newline separators.

#### Scenario: Multiple text blocks joined

- GIVEN `"content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]`
- WHEN loaded via `ChatMessage.UnmarshalJSON`
- THEN `Content.TextOnly() == "part1\npart2"`

---

### Requirement: Mixed Text+Image Array — Text Preserved, Image Noted

When the array contains both text and image blocks, `TextOnly()` SHALL return the text portions. Image blocks SHALL contribute a `"[image]"` placeholder so the reading context is not silently lost.

#### Scenario: Mixed array read by forward-compat shim

- GIVEN `"content":[{"type":"image","media_sha256":"abc","mime":"image/jpeg","size":1024},{"type":"text","text":"caption"}]`
- WHEN loaded via `ChatMessage.UnmarshalJSON`
- THEN `Content` contains one image block and one text block
- AND `Content.TextOnly()` returns `"caption"` (image skipped by TextOnly)
- AND callers that need an image-aware summary can iterate `Content` directly

---

### Requirement: Image-Only Array — Placeholder Result

When the array contains only non-text blocks, `TextOnly()` SHALL return `""`. The blocks are preserved in `Content` for image-aware consumers.

#### Scenario: Image-only array

- GIVEN `"content":[{"type":"image","media_sha256":"abc","mime":"image/jpeg","size":2048}]`
- WHEN loaded via `ChatMessage.UnmarshalJSON`
- THEN `Content.TextOnly() == ""`
- AND `Content[0].Type == BlockImage`

---

### Requirement: Old Binary Does Not Auto-Upgrade On Write

When a binary that has the forward-compat shim loads a legacy string record and immediately re-saves the conversation, the written JSON MUST use the new array form. The old string form SHALL NOT be written by any new binary. Rollback to a truly old binary (pre-shim) after any new binary has written to disk requires a DB restore.

#### Scenario: Load legacy, re-save writes new array form

- GIVEN a `ChatMessage` loaded from `"content":"hello"` via the shim
- WHEN the conversation is re-saved (marshalled back to JSON)
- THEN the stored `content` field is a JSON array: `[{"type":"text","text":"hello"}]`
- AND NOT the legacy string form

#### Scenario: Pre-shim binary cannot load array form

- GIVEN a conversation row where `content` is a JSON array
- AND the reading binary predates the forward-compat shim (content field is typed `string`)
- WHEN the binary attempts to unmarshal the row
- THEN an unmarshal error occurs
- AND this confirms the shim MUST be deployed before any writer produces the array form
