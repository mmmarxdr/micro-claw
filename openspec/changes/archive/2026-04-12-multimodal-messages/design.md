# Design: Multimodal Messages

## 1. Architecture Overview

Content blocks are the lingua franca. The channel layer downloads media and constructs `[]ContentBlock`. The agent loop appends those blocks verbatim to the conversation. Providers translate blocks to their native wire format at the `Chat()` boundary and nowhere else. Blobs are persisted in a content-addressed store (CAS) keyed by SHA256; conversations reference blobs by hash, not by bytes.

```
                        ┌────────────────────────────────┐
                        │ Telegram / WhatsApp / Discord  │
                        │        (channel layer)         │
                        │                                │
 incoming update ──────►│  1. reject oversized by hdr    │
                        │  2. download bytes             │
                        │  3. store.StoreMedia(sha256)   │
                        │  4. build []ContentBlock       │
                        └───────────────┬────────────────┘
                                        │ IncomingMessage{Content: [blocks]}
                                        ▼
                        ┌────────────────────────────────┐
                        │        agent.loop              │
                        │                                │
                        │  conv.Messages = append(       │
                        │    ChatMessage{Role:"user",    │
                        │                Content:blocks})│
                        │                                │
                        │  if !prov.SupportsMultimodal() │
                        │      flatten → text + degrade  │
                        └───────────────┬────────────────┘
                                        │ ChatRequest
                                        ▼
                        ┌────────────────────────────────┐
                        │        provider.Chat           │
                        │                                │
                        │  Anthropic / OpenAI / Gemini:  │
                        │    translate blocks → native   │
                        │  Ollama:                       │
                        │    flatten to text             │
                        └───────────────┬────────────────┘
                                        │
            ┌───────────────────────────┴────────────┐
            ▼                                        ▼
  ┌──────────────────┐                     ┌────────────────────┐
  │  LLM API         │                     │  store (sqlite)    │
  │  (images inline) │                     │  conversations +   │
  └──────────────────┘                     │  media_blobs (CAS) │
                                           └────────────────────┘
```

Key invariants:
- The agent loop NEVER speaks a provider-specific dialect.
- `ContentBlock.MediaSHA256` is the only cross-boundary media pointer. Bytes are loaded from the store on demand at the provider boundary.
- Media blobs are additive: same photo sent twice = one row in `media_blobs` (dedup).

---

## 2. Type Definitions (Final Go Contract)

All new types live in a new package `internal/content` so channel, provider, store, and agent can import it without cycles. The package is deliberately tiny — just the content-block algebra.

### 2.1 `internal/content/block.go` (NEW)

```go
package content

import "encoding/json"

// BlockType is the discriminator for ContentBlock.
type BlockType string

const (
    BlockText     BlockType = "text"
    BlockImage    BlockType = "image"
    BlockAudio    BlockType = "audio"
    BlockDocument BlockType = "document"
)

// ContentBlock is one element of a multimodal message. It is the canonical
// internal representation; providers translate to their native wire format.
//
// Invariants:
//   - Type == BlockText    → Text is set, media fields are empty.
//   - Type != BlockText    → MediaSHA256, MIME, Size are set; Text is empty.
//   - MediaSHA256 is a lowercase hex SHA256 pointing at store.MediaStore.
type ContentBlock struct {
    Type BlockType `json:"type"`

    // Text content — only set when Type == BlockText.
    Text string `json:"text,omitempty"`

    // Media reference — only set when Type != BlockText.
    // SHA256 of the blob stored in the media_blobs table.
    MediaSHA256 string `json:"media_sha256,omitempty"`
    MIME        string `json:"mime,omitempty"`
    Size        int64  `json:"size,omitempty"`

    // Optional display hint, useful for documents ("invoice-2026-04.pdf").
    Filename string `json:"filename,omitempty"`
}

// Blocks is a slice helper with ergonomic accessors.
type Blocks []ContentBlock

// TextOnly returns all text blocks concatenated with newlines. Non-text blocks
// are silently skipped. Intended for legacy call sites that only need text.
func (bs Blocks) TextOnly() string {
    var out string
    for _, b := range bs {
        if b.Type == BlockText && b.Text != "" {
            if out != "" {
                out += "\n"
            }
            out += b.Text
        }
    }
    return out
}

// HasMedia reports whether any block is non-text.
func (bs Blocks) HasMedia() bool {
    for _, b := range bs {
        if b.Type != BlockText {
            return true
        }
    }
    return false
}

// TextBlock constructs a single-text Blocks slice. Convenience for legacy
// callers and tests.
func TextBlock(s string) Blocks {
    return Blocks{{Type: BlockText, Text: s}}
}

// UnmarshalBlocks accepts either a JSON string (legacy) or a JSON array (modern)
// and returns a normalized Blocks slice. Used by both ChatMessage and
// IncomingMessage/OutgoingMessage UnmarshalJSON shims.
func UnmarshalBlocks(raw json.RawMessage) (Blocks, error) {
    if len(raw) == 0 || string(raw) == "null" {
        return nil, nil
    }
    // Legacy: "content":"hello"
    if raw[0] == '"' {
        var s string
        if err := json.Unmarshal(raw, &s); err != nil {
            return nil, err
        }
        return TextBlock(s), nil
    }
    // Modern: "content":[{...}, ...]
    var bs Blocks
    if err := json.Unmarshal(raw, &bs); err != nil {
        return nil, err
    }
    return bs, nil
}
```

### 2.2 `internal/channel/channel.go` (MODIFIED)

```go
import "microagent/internal/content"

type IncomingMessage struct {
    ID        string
    ChannelID string
    SenderID  string
    Content   content.Blocks    // REPLACES Text string
    Metadata  map[string]string
    Timestamp time.Time
}

// Text returns the concatenated text from all text blocks. Preserved for
// call sites that don't care about media (logs, metrics, legacy paths).
// Deprecated in favor of direct Content iteration, but intentionally kept
// to minimize churn.
func (m IncomingMessage) Text() string { return m.Content.TextOnly() }

// OutgoingMessage stays TEXT-ONLY for this change (per Q2 — outgoing
// multimodal deferred). A future change will introduce Content blocks here
// with the corresponding channel send-path work.
type OutgoingMessage struct {
    ChannelID   string
    RecipientID string
    Text        string            // unchanged
    Metadata    map[string]string
}
```

Rationale for Q2 deferral: doubling the surface area (send path per channel) triples test load. The receive path is where every UX complaint lives today. Outgoing is a clean additive follow-up once the content-block pipeline is battle-tested.

### 2.3 `internal/provider/provider.go` (MODIFIED)

```go
import "microagent/internal/content"

type ChatMessage struct {
    Role       string         `json:"role"`
    Content    content.Blocks `json:"content"` // REPLACES string
    ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
    ToolCallID string         `json:"tool_call_id,omitempty"`
}

// UnmarshalJSON accepts both the legacy string form ("content":"hi") and the
// modern block array ("content":[{...}]). MarshalJSON always emits the array.
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
    var raw struct {
        Role       string          `json:"role"`
        Content    json.RawMessage `json:"content"`
        ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
        ToolCallID string          `json:"tool_call_id,omitempty"`
    }
    if err := json.Unmarshal(data, &raw); err != nil {
        return err
    }
    m.Role = raw.Role
    m.ToolCalls = raw.ToolCalls
    m.ToolCallID = raw.ToolCallID
    blocks, err := content.UnmarshalBlocks(raw.Content)
    if err != nil {
        return err
    }
    m.Content = blocks
    return nil
}

// Provider interface gains two capability methods.
type Provider interface {
    Name() string
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    SupportsTools() bool
    SupportsMultimodal() bool // NEW — true iff provider can receive images
    SupportsAudio() bool      // NEW — narrower: true iff provider can receive audio (Gemini, GPT-4o)
    HealthCheck(ctx context.Context) (string, error)
}
```

Helper on `ChatMessage`:

```go
// TextOnly concatenates all text blocks. Same helper as content.Blocks.TextOnly
// — exposed on ChatMessage for call-site ergonomics.
func (m ChatMessage) TextOnly() string { return m.Content.TextOnly() }
```

### 2.4 Backward-compat helper `IncomingMessage.TextOnly()`

Already provided via the `Text()` method above. Logs and metrics that currently read `msg.Text` become `msg.Text()` with zero semantic change for text-only messages.

---

## 3. Provider Capability + Multimodal Translation

### 3.1 Capability matrix (as of cutoff)

| Provider   | SupportsMultimodal | SupportsAudio | Notes |
|------------|--------------------|---------------|-------|
| Anthropic  | true               | false         | Claude 3.5/4 accept images; audio not GA |
| OpenAI     | true (gpt-4o+)     | true (gpt-4o) | model-dependent; hard-code to true and let API errors drive per-model fallback in a future change |
| Gemini     | true               | true          | `inlineData` accepts both |
| OpenRouter | true               | true          | passthrough to upstream model |
| Ollama     | false              | false         | local; vision support is model-specific, assume false for safety |
| Fallback   | min of members     | min of members| must AND over its pool |

**Discovery**: OpenAI's capability is actually per-model (gpt-4o yes, gpt-3.5 no). The provider interface returns a static `true` and trusts the runtime API to reject. A richer per-model check is deferred — design note only.

### 3.2 Native translation rules

**Anthropic** (`internal/provider/anthropic_stream.go` — `buildAnthropicRequest`):

```go
for _, m := range req.Messages {
    blocks := make([]any, 0, len(m.Content))
    for _, b := range m.Content {
        switch b.Type {
        case content.BlockText:
            blocks = append(blocks, map[string]any{
                "type": "text", "text": b.Text,
            })
        case content.BlockImage:
            data, _, err := mediaStore.GetMedia(ctx, b.MediaSHA256)
            if err != nil { /* fall through to degradation text */ continue }
            blocks = append(blocks, map[string]any{
                "type": "image",
                "source": map[string]any{
                    "type":       "base64",
                    "media_type": b.MIME,
                    "data":       base64.StdEncoding.EncodeToString(data),
                },
            })
        // audio & document → degradation text (Anthropic no audio)
        }
    }
    // ... append to messages array
}
```

**OpenAI** (`internal/provider/openai.go` — `buildOpenAIRequest`):
```json
{"role":"user","content":[
  {"type":"text","text":"..."},
  {"type":"image_url","image_url":{"url":"data:image/jpeg;base64,..."}}
]}
```

**Gemini** (`internal/provider/gemini.go` — `buildGeminiRequest`):
```json
{"role":"user","parts":[
  {"text":"..."},
  {"inlineData":{"mimeType":"image/jpeg","data":"<base64>"}}
]}
```

**Ollama** (`internal/provider/ollama.go`): flatten every non-text block to the degradation placeholder and append as text.

### 3.3 Graceful degradation

When `!prov.SupportsMultimodal()` OR a specific block type is unsupported (e.g. audio on Anthropic):

1. Provider's builder replaces the block with a text block containing:

   ```
   [image attached: photo.jpg, 1.2 MB, MIME image/jpeg, not processed by current model]
   ```

   Format template (in `internal/content/degrade.go`):
   ```go
   func DegradeBlock(b ContentBlock) string {
       name := b.Filename
       if name == "" { name = string(b.Type) }
       return fmt.Sprintf("[%s attached: %s, %s, MIME %s, not processed by current model]",
           b.Type, name, humanSize(b.Size), b.MIME)
   }
   ```

2. The agent loop flags `degraded = true` on the request and, upon receiving the assistant reply, **prepends** the user notice:

   ```
   (I can't see images with the current model. I saved it for you.)
   ```

   Or for audio:
   ```
   (I can't listen to voice notes with the current model. I saved it for you.)
   ```

   Message-type-aware notice selection lives in `internal/agent/loop.go` in a new `degradationNotice(blocks) string` helper.

---

## 4. Store Design — CAS (Q1 → P3)

### 4.1 Schema — `media_blobs` (migration v5)

```sql
CREATE TABLE IF NOT EXISTS media_blobs (
    sha256              TEXT PRIMARY KEY,   -- lowercase hex
    mime                TEXT NOT NULL,
    size                INTEGER NOT NULL,
    data                BLOB NOT NULL,
    created_at          DATETIME NOT NULL,
    last_referenced_at  DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_media_blobs_lastref
    ON media_blobs(last_referenced_at);
```

Lives in a new migration step `migrateV5()` in `internal/store/migration.go`. Additive only — no changes to `conversations`. Rollback = drop the table (it's not referenced by any foreign key).

### 4.2 `MediaStore` interface (new file `internal/store/media.go`)

```go
type MediaStore interface {
    // StoreMedia hashes data, upserts the blob, and returns the lowercase
    // hex SHA256. Dedup is automatic — storing the same bytes twice is a no-op
    // that still refreshes last_referenced_at.
    StoreMedia(ctx context.Context, data []byte, mime string) (sha256 string, err error)

    // GetMedia retrieves bytes + MIME for a previously stored blob.
    // Returns ErrNotFound if the hash is unknown.
    GetMedia(ctx context.Context, sha256 string) (data []byte, mime string, err error)

    // TouchMedia updates last_referenced_at to now. Called when a
    // conversation referencing this blob is loaded OR saved.
    TouchMedia(ctx context.Context, sha256 string) error

    // PruneUnreferencedMedia deletes blobs whose last_referenced_at is older
    // than olderThan AND whose sha256 does not appear in any live
    // conversations.messages JSON. Returns the number of rows deleted.
    PruneUnreferencedMedia(ctx context.Context, olderThan time.Duration) (int, error)
}
```

Implemented by `*SQLiteStore`. No separate impl for `FileStore` — `FileStore` is the JSON-only dev fallback and explicitly returns `ErrNotImplemented` for `StoreMedia` (the agent logs a warning and degrades to "media disabled on file store").

### 4.3 Pruning query

```sql
DELETE FROM media_blobs
WHERE last_referenced_at < ?
  AND sha256 NOT IN (
      SELECT DISTINCT json_extract(value, '$.media_sha256')
      FROM conversations, json_each(json_extract(messages, '$'))
      WHERE json_extract(value, '$.media_sha256') IS NOT NULL
  );
```

The JSON scan is expensive but runs once per `cleanup_interval` (default 24h). At micro-claw's expected scale (thousands of conversations, not millions) it's fine. The index on `last_referenced_at` narrows the candidate set before the JSON scan.

### 4.4 SQLite BLOB concerns

- SQLite BLOB max = 1 GB theoretical; our config cap is 10 MB per attachment, 25 MB per message.
- WAL: large inserts take a WAL page hit; we accept it. Pruning runs outside the hot path.
- `sqlite_dump` backups will grow. Acceptable — user can exclude `media_blobs` if they want a light backup.
- No compression in v1 (photos are already compressed; voice notes are OGG Opus which is compressed). Design note: reevaluate if users complain about DB size.

### 4.5 Reference tracking

When `LoadConversation` deserialises messages, it walks blocks and calls `TouchMedia(sha)` for every media block. Same on `SaveConversation`. This keeps `last_referenced_at` fresh for any blob that's still reachable from a live conversation. Batch the touches in a single UPDATE:

```sql
UPDATE media_blobs SET last_referenced_at = ? WHERE sha256 IN (?, ?, ...);
```

---

## 5. Channel Layer — Media Download

### 5.1 Telegram (`internal/channel/telegram.go`)

Telegram updates carry `FileID` pointers, not bytes. Two-stage flow:

1. **Pre-download size gate.** `update.Message.Photo[len-1].FileSize` is in the update; reject if over `max_attachment_bytes` and reply to user with size error. NO network call wasted.
2. **Download.** `url, err := bot.GetFileDirectURL(fileID)` → `http.Get(url)` → bytes.
3. **Store.** `sha, err := store.StoreMedia(ctx, bytes, mime)`.
4. **Build.** `content.Blocks{{Type: BlockImage, MediaSHA256: sha, MIME: mime, Size: size, Filename: originalName}}`.
5. **Caption.** Prepend `update.Message.Caption` as a text block if non-empty.
6. **Enqueue.** `inbox <- IncomingMessage{Content: blocks, ...}`.

**Error handling.** If step 2 or 3 fails, the channel still enqueues:
- A text block with `(media failed to download: <reason>)`
- Plus any caption text
- So the user is never left wondering.

**MIME detection.** Telegram gives `MimeType` on documents/voice directly. For photos (no MIME field) → `image/jpeg` default, verified via `http.DetectContentType` on first 512 bytes.

**Voice notes.** `update.Message.Voice.MimeType = "audio/ogg"` (OGG Opus). MIME flows through to the store unchanged.

**Documents.** `update.Message.Document.MimeType` carried through. Whitelist check against `media.allowed_mime_prefixes` before download — unrecognized MIME → reject with notice.

### 5.2 WhatsApp (`internal/channel/whatsapp.go`) — Phase 8

Similar flow, extra hop: webhook gives `media_id` → call Graph API `GET /{media_id}` → get download URL → HTTP GET with bearer token. Document the two-stage retrieval; defer implementation to Phase 8 per proposal.

### 5.3 Discord (`internal/channel/discord.go`) — Phase 8

`m.Attachments[]` already contains direct URLs. Download, store, build blocks. Simplest of the three.

### 5.4 CLI (`internal/channel/cli.go`)

Accept a line of form:

```
/attach <absolute-path-or-tilde>
[optional text prompt]
```

Parser:
- Line starts with `/attach` → read the path, `os.ReadFile`, detect MIME via `http.DetectContentType`, store, build a block.
- Next lines (until blank) accumulate as text block.
- Enqueue combined message.

Implementation in Phase 8. Useful for smoke tests without a real chat platform.

---

## 6. Agent Loop Changes

### 6.1 `internal/agent/loop.go — processMessage`

```go
slog.Debug("processing message",
    "channel_id", msg.ChannelID,
    "sender_id", msg.SenderID,
    "block_count", len(msg.Content),
    "text_len",   len(msg.Content.TextOnly()),
    "has_media",  msg.Content.HasMedia(),
)

conv.Messages = append(conv.Messages, provider.ChatMessage{
    Role:    "user",
    Content: msg.Content, // whole block slice, no conversion
})
```

### 6.2 `msg.Text` call-site inventory (to convert)

| File | Current | Replacement |
|------|---------|-------------|
| `internal/agent/loop.go:34` | `"text_len", len(msg.Text)` | `"text_len", len(msg.Content.TextOnly())` |
| `internal/agent/loop.go:53` | `Content: msg.Text` | `Content: msg.Content` |
| `internal/agent/loop.go:67` | `a.store.SearchMemory(..., msg.Text, ...)` | `a.store.SearchMemory(..., msg.Content.TextOnly(), ...)` |
| `internal/agent/agent.go:214` | `truncate(m.Text, 80)` | `truncate(m.Content.TextOnly(), 80)` |
| all test fixtures in `loop_test.go`, `integration_test.go` | `msg.Text = "..."` | `msg.Content = content.TextBlock("...")` |

Use `content.TextBlock(s)` as the migration shim for test construction — keeps diffs minimal.

### 6.3 Degradation check point

Exactly ONE place in `loop.go`, right before `provider.Chat()`:

```go
degraded := false
if !a.provider.SupportsMultimodal() && anyBlockIsMedia(conv.Messages) {
    degraded = true
}
resp, err := a.provider.Chat(ctx, req)
// ... after success:
if degraded {
    notice := degradationNotice(conv.Messages)
    resp.Content = notice + "\n\n" + resp.Content
}
```

Note: `anyBlockIsMedia` walks only the NEW user message (last one), not full history, because earlier-turn media was already flattened on prior turns.

### 6.4 Provider translation happens ONCE

In each provider's `buildRequest`. The agent loop never constructs Anthropic/OpenAI/Gemini payload shapes. This is already the case today for text — we preserve the discipline.

---

## 7. Filter Layer — Scope Clarification

**Q3 re-scoping.** The user's original Q3 was about `internal/filter/filter.go`, but that file filters **tool output**, not channel input. Tool output is (and remains) a single `string`. Therefore:

- **`internal/filter/filter.go`: ZERO changes in this proposal.** No iteration over content blocks. No new behavior. Tool outputs stay text.
- The real "passthrough non-text, filter text" surface is the **context builder** in `internal/agent/context.go` — but it already operates on `ChatMessage` which will natively carry `content.Blocks`. When the builder truncates/summarizes history, it must be aware of blocks. Specifically:
  - **Token counting**: a media block counts as a provider-specific fixed cost (Anthropic: ~1500 tokens per image; OpenAI: 85 + tiles; Gemini: 258). Encode as a constant `content.BlockTokenEstimate(b, providerName)` so the token manager can budget.
  - **Summarisation**: when an older turn is summarised, media blocks flatten to their degradation text before the summarisation provider sees them. The summary itself is always a text block.
  - **Truncation**: oldest-first drop does not split a single message's blocks — drop whole messages.

- **Future-proofing**: if and when tool outputs become multimodal (out of scope, separate change), `filter.Apply` will need to iterate blocks. Leave a `// TODO(multimodal-tool-output)` comment anchor in `filter.go` to mark the spot.

**Design rule**: `filter.go` PreApply/Apply signatures do NOT change. Only comments are added.

---

## 8. Config Schema

New section in `internal/config/config.go`:

```go
type AgentConfig struct {
    // ... existing fields ...
    Media MediaConfig `yaml:"media"`
}

type MediaConfig struct {
    Enabled             bool          `yaml:"enabled"`                // default true
    MaxAttachmentBytes  int64         `yaml:"max_attachment_bytes"`   // default 10_485_760  (10 MB)
    MaxMessageBytes     int64         `yaml:"max_message_bytes"`      // default 26_214_400  (25 MB)
    RetentionDays       int           `yaml:"retention_days"`         // default 30
    CleanupInterval     time.Duration `yaml:"cleanup_interval"`       // default 24h
    AllowedMIMEPrefixes []string      `yaml:"allowed_mime_prefixes"`  // default: image/, audio/, application/pdf, text/
}

// DefaultMediaConfig returns the defaults used when the YAML is silent.
func DefaultMediaConfig() MediaConfig { ... }
```

YAML:

```yaml
agent:
  media:
    enabled: true
    max_attachment_bytes: 10485760
    max_message_bytes: 26214400
    retention_days: 30
    cleanup_interval: 24h
    allowed_mime_prefixes:
      - "image/"
      - "audio/"
      - "application/pdf"
      - "text/"
```

### 8.1 Validation rules (in `config.Validate()`)

- `MaxAttachmentBytes` in `[1 KB, 50 MB]` — outside that is an error.
- `MaxMessageBytes >= MaxAttachmentBytes` — else error.
- `RetentionDays >= 1` — else error.
- `CleanupInterval >= 1h` — else error (cheap guardrail).
- `AllowedMIMEPrefixes` — if empty and `Enabled == true`, fail with a clear "whitelist is empty, no media will be accepted; set agent.media.enabled: false to disable explicitly."

### 8.2 Retention job

A new goroutine in `agent.Agent.Start()`:

```go
go a.mediaCleanupLoop(ctx) // ticks every cfg.CleanupInterval, calls PruneUnreferencedMedia
```

Logs count of pruned blobs at INFO. Respects context cancel.

---

## 9. Lazy JSON Shim (Q4) — Exact Implementation

### 9.1 `ChatMessage.UnmarshalJSON` (see §2.3 above for full code)

Core logic:
```go
if len(raw.Content) > 0 && raw.Content[0] == '"' {
    // Legacy string form
    var s string
    if err := json.Unmarshal(raw.Content, &s); err != nil {
        return err
    }
    m.Content = content.TextBlock(s)
    return nil
}
// Modern array form
return json.Unmarshal(raw.Content, &m.Content)
```

### 9.2 No custom `MarshalJSON`

`ChatMessage` uses the default struct marshaller which emits `"content":[...]` — the new shape. **Write-forward, read-both.** The one-way write guarantees that once a conversation is re-saved, it's in the new shape forever. Old binaries reading new JSON will FAIL — document this as the "forbidden rollback" scenario (see §11).

### 9.3 Fixture test — migration canary

Check in `internal/provider/testdata/legacy_chatmessage.json`:

```json
{"role":"user","content":"hello from before multimodal"}
```

Test asserts:
- Unmarshal succeeds
- `msg.Content == Blocks{{Type: BlockText, Text: "hello from before multimodal"}}`
- Re-marshal produces `{"role":"user","content":[{"type":"text","text":"hello from before multimodal"}]}`
- That re-marshalled JSON re-unmarshals to the same `Blocks`

This test is the migration proof. It must pass before phase 1 merges and must never be deleted.

---

## 10. Test Strategy

| Layer | What | How |
|-------|------|-----|
| Unit — content | `UnmarshalBlocks` both shapes | Table test: string input → text block; array input → multi-block |
| Unit — content | `Blocks.TextOnly()`, `HasMedia()` | Pure-function table test |
| Unit — provider/anthropic | block → native wire | Golden JSON file under `testdata/anthropic_multimodal.json` |
| Unit — provider/openai | block → `image_url` | Golden JSON file |
| Unit — provider/gemini | block → `inlineData` | Golden JSON file |
| Unit — provider/ollama | degradation flattening | Assert text-only body contains `[image attached: ...]` |
| Unit — provider | `SupportsMultimodal`, `SupportsAudio` | Trivial constant-return tests |
| Unit — store | `StoreMedia` dedup | Store same bytes twice, assert one row, assert `last_referenced_at` refreshed |
| Unit — store | `PruneUnreferencedMedia` | Seed 3 blobs, 1 referenced in conv, 2 unreferenced (one fresh, one stale), assert only 1 deleted |
| Unit — channel/telegram | media → blocks | Fake bot returning canned bytes; assert `IncomingMessage.Content` |
| Unit — channel/telegram | oversized rejection | File size > limit → assert no download attempted, user gets size error |
| Unit — channel/telegram | download failure → text-only enqueue | Fake bot returning 500; assert text block with failure notice |
| Unit — filter | NO test changes | Signature unchanged |
| Unit — agent/loop | legacy `msg.Text` → `msg.Content.TextOnly()` | Existing tests rewritten via `content.TextBlock()` helper |
| Unit — agent/loop | degradation notice on non-multimodal provider | Fake provider with `SupportsMultimodal=false`, assert reply prefixed |
| Integration — round trip | Telegram fixture → Anthropic mock → store | End-to-end: assert body sent to mock contains base64 image block |
| Integration — legacy conversation | On-disk legacy JSON file → load → re-save → re-load | Use checked-in fixture; assert semantic equality |
| Integration — retention GC | Fast-forward clock, call cleanup, assert blob rows deleted | Use clock injection helper in `store` |
| CI rehearsal | Full suite green under `-race -count=1` | Standard `go test ./...` gate |

New fixtures: `internal/provider/testdata/legacy_chatmessage.json`, `internal/channel/testdata/telegram_photo.bin`, `internal/provider/testdata/anthropic_multimodal_request.json`.

---

## 11. Rollback Plan (Expanded)

| Rollback window | What works | What breaks | Action |
|-----------------|------------|-------------|--------|
| **Pre-phase-1** (before the type refactor lands) | Everything | Nothing | `git revert` the single commit. Zero risk. |
| **Post-phase-1, pre-phase-6** (types landed, migration fixture not yet proven) | New binaries read both old and new JSON | **Old binaries cannot read new JSON** — if any conversation has been re-saved, it's now `{"content":[...]}` which old binaries will fail to unmarshal on `string` | Forward-compat reader MUST ship on main BEFORE any code in phase 1 is allowed to re-serialize. Concretely: a commit that adds only `UnmarshalBlocks` acceptance on the current `string`-typed `ChatMessage.Content` (via a temporary parallel type) is merged first. Alternative: if that's too intrusive, accept that once phase 1 lands, rollback requires a DB restore from backup. Document this loudly. |
| **Post-phase-6** (migration canary test passing) | New is the wire format | Old binaries permanently broken against new DB | Rollback IS the DB restore. The shim is forward-one-way by design. |
| **Any phase, feature disable** | N/A — not a code rollback | N/A | Set `agent.media.enabled: false` in config; channels stop downloading media. Type system stays migrated but no new media enters. This is the cheap "turn it off" knob. |

**Forbidden rollback action**: reverting past phase 1 without a full DB backup. Old binaries cannot read the new JSON shape — they will panic-or-error on every conversation load.

**Mitigation for forbidden rollback**: ship a dedicated read-only "old-shape" compatibility binary as a side artifact in the phase 1 release, so operators holding a new DB can still export their conversations to the old shape if they need to roll back hard. This is belt-and-suspenders; likely unneeded for a personal single-user binary but cheap to provide.

---

## 12. Open Risks + Mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Binary blob size inflation in SQLite | High | 10 MB/attachment cap at channel edge; CAS dedup; retention GC; `agent.media.enabled: false` kill switch |
| JSON shim breaks on edge cases (null content, `"content":null`, empty array) | Medium | `UnmarshalBlocks` handles nil + null + empty explicitly; table tests cover every edge |
| Provider capability lies (OpenAI says multimodal but current model is gpt-3.5) | Medium | Static `true` on OpenAI provider; trust API 400 to surface; future change introduces per-model capability |
| Fallback provider capability min-over-pool is wrong if pool is heterogeneous | Low | Document: `Fallback.SupportsMultimodal` returns false if any member lacks it. Safer under-statement. |
| Pruning query scans all conversations JSON on every interval | Low (at our scale) | Indexed `last_referenced_at` narrows candidate set; runs once per 24h; revisit if DB grows past ~10 GB |
| Telegram download timeout stalls channel goroutine | Medium | Per-download `context.WithTimeout(30s)`; on timeout → drop media, enqueue text-only with notice |
| Memory pressure from holding a 25 MB blob across a 30 s LLM call | Medium | Load bytes from store only at provider build time; `ChatMessage` in memory carries only the hash; blob is GC-ed after the request body is sent |
| `media_blobs` table has no FK into conversations — referential integrity is soft | Accepted | Pruning query does the integrity check lazily; a blob referenced by a conversation will never be pruned (the JSON scan catches it) |
| Legacy JSON fixture drifts from real production JSON shape | Low | Fixture is checked in and reviewed; test is a canary — if it fails CI, we know before shipping |
| Voice note on Anthropic (text-only for audio) feels identical to "dropped" | Low-Med | Degradation notice is audio-specific ("I can't listen to voice notes...") and fires 100% of the time |
| CLI `/attach` parser surprises users with path quoting | Low | Document in help; accept both quoted and unquoted; shell-escape rules are user-handled |
| FileStore does not implement `MediaStore` | Medium | `FileStore.StoreMedia` returns `ErrMediaNotSupported`; channel layer logs and falls through to text-only; users on filestore get a one-line warning at startup |

---

## Architecture Decisions — One-Page Summary

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | Internal message shape | `[]ContentBlock` on `ChatMessage` + `IncomingMessage` | Industry standard; single code path; no parallel `.Attachments` tax |
| 2 | Blob persistence | CAS via `media_blobs(sha256 PK, data BLOB)` | Dedup + integrity + atomic with conversation; future compression/encryption-ready |
| 3 | Outgoing multimodal | Deferred | Keep scope focused on highest-impact UX (receive path); outgoing is clean additive follow-up |
| 4 | Filter layer | No changes | Tool output stays text; content-block awareness belongs in context builder, not filter |
| 5 | JSON migration | Lazy `UnmarshalJSON` shim, write-forward | Zero startup cost; rollback window acceptable for personal binary |
| 6 | Provider capability | `SupportsMultimodal()` + `SupportsAudio()` | Two axes because audio is the narrower subset; static per provider, per-model refinement deferred |
| 7 | Degradation UX | Provider flattens to text placeholder + agent loop prepends user notice | Provider sees enough to reference the attachment; user is always told explicitly |
| 8 | Media namespace | `internal/content` package | Tiny; imported by channel/provider/store/agent; zero cycle risk |
| 9 | CLI multimodal | `/attach <path>` slash command | Smoke-test parity without a real chat platform |
| 10 | Retention | Background goroutine, interval-driven, JSON scan pruning | Scale-appropriate; index on `last_referenced_at` narrows candidates |

---

## File Changes Summary

| File | Action | Purpose |
|------|--------|---------|
| `internal/content/block.go` | CREATE | `ContentBlock`, `Blocks`, `UnmarshalBlocks`, `TextBlock` |
| `internal/content/degrade.go` | CREATE | `DegradeBlock` text template |
| `internal/content/block_test.go` | CREATE | Round-trip + edge cases |
| `internal/channel/channel.go` | MODIFY | `IncomingMessage.Content content.Blocks`, helper `Text()` |
| `internal/channel/telegram.go` | MODIFY | Media download + block construction |
| `internal/channel/whatsapp.go` | MODIFY (Phase 8) | Graph API media resolve + download |
| `internal/channel/discord.go` | MODIFY (Phase 8) | Attachment download |
| `internal/channel/cli.go` | MODIFY (Phase 8) | `/attach` parser |
| `internal/provider/provider.go` | MODIFY | `ChatMessage.Content content.Blocks`, `UnmarshalJSON`, `SupportsMultimodal`, `SupportsAudio` |
| `internal/provider/anthropic_stream.go` | MODIFY | Image block translation |
| `internal/provider/openai.go` + `_stream.go` | MODIFY | `image_url` translation |
| `internal/provider/gemini.go` + `_stream.go` | MODIFY | `inlineData` translation |
| `internal/provider/ollama.go` | MODIFY | Degradation flattening |
| `internal/provider/openrouter.go` + `_stream.go` | MODIFY | Passthrough via OpenAI path |
| `internal/provider/fallback.go` | MODIFY | `SupportsMultimodal` = AND over pool |
| `internal/provider/testdata/legacy_chatmessage.json` | CREATE | Migration canary fixture |
| `internal/store/media.go` | CREATE | `MediaStore` interface |
| `internal/store/sqlitestore.go` | MODIFY | Implement `MediaStore` methods |
| `internal/store/filestore.go` | MODIFY | `MediaStore` stubs → `ErrMediaNotSupported` |
| `internal/store/migration.go` | MODIFY | Add `migrateV5()` — `media_blobs` table |
| `internal/store/media_test.go` | CREATE | Dedup + prune tests |
| `internal/config/config.go` | MODIFY | `MediaConfig`, defaults, validation |
| `internal/config/config_test.go` | MODIFY | New validation cases |
| `internal/agent/loop.go` | MODIFY | `msg.Content` append, degradation notice, cleanup loop launch |
| `internal/agent/agent.go` | MODIFY | `truncate(m.Content.TextOnly(), 80)` |
| `internal/agent/context.go` | MODIFY | Token estimation aware of media blocks |
| `internal/agent/loop_test.go` | MODIFY | `content.TextBlock(...)` migration |
| `internal/agent/integration_test.go` | MODIFY | Multimodal end-to-end |
| `internal/filter/filter.go` | MODIFY (comment only) | `// TODO(multimodal-tool-output)` anchor |

Counts: **7 new files**, **~20 modified files**, **0 deletions**. Aligned with the proposal's estimate of 15-25 test files touched.

---

## Next Step

Ready for `sdd-tasks` — this design is concrete enough to produce a mechanical task breakdown per the proposal's 10 phases.
