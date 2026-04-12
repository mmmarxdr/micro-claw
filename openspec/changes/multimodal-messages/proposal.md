# Proposal: Multimodal Messages

## 1. Why

Users on Telegram, WhatsApp, and Discord routinely send photos, voice notes, and documents. micro-claw silently drops every non-text update at the channel edge, which is the single biggest UX gap right now — voice notes never get heard, photos never get seen, and the user gets no acknowledgement. The providers we already ship (Anthropic, OpenAI, Gemini) natively accept images and audio; we are leaving their capability on the floor. Fix it now so downstream work (personal-assistant integrations, agentic memory) can assume the agent can perceive what the user actually sends.

## 2. Scope

### In Scope

- **Core type evolution** — `IncomingMessage`, `OutgoingMessage`, and `provider.ChatMessage` carry `[]ContentBlock` instead of `Text string` / `Content string`. `ContentBlock` is a discriminated union: `text | image | audio | document`.
- **Provider capability** — `provider.Provider` gains `SupportsMultimodal() bool` (or a richer capability struct); agent loop respects it.
- **Anthropic / OpenAI / Gemini multimodal body builders** — each provider translates `[]ContentBlock` to its native wire format (Anthropic `image` blocks, OpenAI `image_url`, Gemini `inlineData`). Audio blocks go to Gemini and GPT-4o when supported.
- **Graceful degradation** — text-only providers receive a synthesized text stand-in (`[image attached: photo.jpg, 1.2 MB, not processed by current model]`) AND the agent appends a one-line user-facing notice to its reply (`"I can't actually see images with the current model. I saved it for you."`).
- **Telegram channel** — download `.Message.Photo`, `.Message.Voice`, `.Message.Document` via `bot.GetFileDirectURL` + HTTP fetch, build content blocks, preserve caption as a text block.
- **WhatsApp channel** — resolve `media_id` via Graph API, download bytes, build content blocks. Handle image/audio/document message types.
- **Discord channel** — extract `m.Attachments`, download via URL, build content blocks.
- **CLI channel** — stub-accept a simple `/attach <path>` syntax so the CLI can smoke-test multimodal without relying on a real chat platform.
- **Store** — persistence strategy for media blobs (TBD — see open question 1), cleanup/retention job, size accounting on write.
- **Config** — `multimodal.max_attachment_bytes` (default 10 MB), `multimodal.max_message_bytes` (default 25 MB), `multimodal.retention_days` (default 30), per-channel enable flag.
- **Voice notes — privacy path** — raw audio forwarded to multimodal providers that accept audio. **No external transcription services (Whisper, etc.).** Unsupported → graceful-degradation path.
- **Conversation JSON migration** — wire-format back-compat for existing stored `{"content":"..."}` records (TBD — see open question 4).
- **Filter layer** — decide passthrough vs. skip behavior for non-text blocks in `internal/filter/filter.go` (TBD — see open question 3).
- **Tests** — update construction sites across channels/providers/agent/store and add multimodal-specific coverage (round-trip, degradation notice, size-limit rejection, retention-job deletion).

### Out of Scope (explicit)

- **Drop-on-full inbox fix** — `inbox <- msg` default-drop is a pre-existing bug (telegram.go:129, discord.go). Multimodal will make it louder, but fixing it is a separate change.
- **Permission / approval flow** — no "do you want me to process this image?" gate.
- **Async long-running task model** — if a multimodal call takes 30s, it blocks a semaphore slot the same as a text call today. Worker-pool redesign is a separate change.
- **External transcription services (Whisper, Deepgram, Eleven, etc.)** — explicitly rejected for privacy. User decision, non-negotiable.
- **Outgoing multimodal** — deferred unless open question 2 resolves otherwise.
- **Personal-assistant integrations** (Gmail, GCal, etc.) — come after this change, not inside it.
- **UI / rendering of media** — no web UI, no TUI thumbnails. Store has bytes; display is somebody else's problem.

## 3. Capabilities

### New Capabilities
- `multimodal-messages`: core capability — content-block message model, provider multimodal translation, graceful degradation, media persistence, retention.

### Modified Capabilities
- `provider-interface`: adds `SupportsMultimodal()` and changes `ChatMessage.Content` from `string` to `[]ContentBlock`. Existing providers must implement the capability check and translate blocks.
- `channel-interface`: `IncomingMessage` / `OutgoingMessage` evolve from `Text string` to `[]ContentBlock`; channels gain media-download responsibilities.
- `conversation-store`: stored JSON shape changes; forward/backward compat shim required.

(If `openspec/specs/` does not yet contain these three capabilities as tracked specs, sdd-spec should create them — exploration shows these layers exist in code but have never been specified.)

## 4. Approach

**Approach A — Content blocks on core types.** User-locked. The exploration recommended sidecar attachments (Approach B) for lower churn, but the user explicitly chose A because content blocks are the industry standard (Anthropic and OpenAI both model conversation payloads this way) and because two parallel code paths (`.Content` + `.Attachments`) is a permanent tax we don't want to carry.

**Shape of `ContentBlock` (sketch, not design):**

```go
type ContentBlock struct {
    Type       BlockType // text | image | audio | document
    Text       string    // type=text
    MIME       string    // type=image|audio|document
    Data       []byte    // inline bytes OR empty if Ref is set
    Ref        string    // store reference (hash/path, chosen persistence strategy)
    Filename   string    // display hint
    SizeBytes  int64
}
```

The final shape is design-phase work — the proposal commits only to "discriminated union of typed blocks with a way to point at bytes that may be stored externally."

**Translation layer.** Providers own the translation from `[]ContentBlock` to their wire format. The agent loop never speaks Anthropic/OpenAI/Gemini dialects — it hands blocks to the provider and the provider decides. This is the critical architectural rule: **content blocks are the lingua franca; providers are the translators.**

**Text-only providers.** When `provider.SupportsMultimodal() == false`, the provider's message builder replaces each non-text block with a text placeholder describing the attachment (type, filename, size). The agent loop separately prepends or appends a single-line notice to the assistant's reply so the user knows the media was not consumed. Both degradations happen — the provider sees enough to mention "you attached a photo" if the LLM decides to, and the user always gets told their media was saved but not processed.

**Voice notes.** Identical pipeline — audio is just another block type. Providers that accept audio (Gemini, GPT-4o audio, future Anthropic audio) translate to native audio. Providers that don't hit the degradation path. **No external transcription service is ever called.** This is a firm boundary: privacy > cost > convenience.

**Layer impact at a glance:**

```
channel (download media, build blocks)
    → agent loop (append IncomingMessage as user ChatMessage with []ContentBlock)
        → context builder (passthrough; blocks are native to ChatMessage)
            → provider.SendChat (translate blocks OR degrade)
                → store (persist conv.Messages JSON, blobs via chosen strategy)
```

Nothing bypasses the block abstraction once it exists.

## 5. Open Questions

These require a user decision before sdd-design / sdd-spec can lock the architecture.

### Q1. Blob persistence strategy

| Option | What it is | Pros | Cons |
|--------|-----------|------|------|
| **P1. Inline BLOB in `conversations`** | Bytes live in the messages JSON column | Simplest. Atomic with the conversation. One table. | DB grows fast (10 MB photo per turn). `sqlite_dump` / backups get heavy. Page cache thrashes. |
| **P2. External files** | `~/.microagent/data/media/{conv_id}/{attachment_id}.{ext}`; JSON stores the path | Fast. Cheap backups (exclude dir). Easy cleanup (rm dir). | Not atomic — crash between DB commit and file write loses integrity. Orphan files on partial failure. Path portability on user-dir moves. |
| **P3. Content-addressed store (CAS)** | `media_blobs(sha256 PRIMARY KEY, bytes BLOB, mime, size, created_at)`; JSON references by hash | Deduplication (same photo forwarded twice = one row). Integrity (hash verifies content). Atomic (single DB txn). Retention = `DELETE FROM media_blobs WHERE sha256 NOT IN (SELECT ref FROM conversations) AND created_at < now-30d`. | Most complex to implement. Still inside SQLite, so large-blob tradeoffs still apply (but at least dedup'd). Needs a GC job. |

**Recommendation: P3 (CAS).** Integrity + dedup + atomicity outweigh the extra implementation cost. P1 is a trap at 10 MB photos. P2's atomicity hole is a real correctness bug waiting to bite — we already have a drop-on-full-inbox issue, we don't need a drop-on-crash-during-write issue too. CAS is how Git, IPFS, and every serious content store handle this, and it scales cleanly to future per-message compression or encryption-at-rest.

### Q2. Outgoing multimodal (agent → user)

| Option | Pros | Cons |
|--------|------|------|
| **Defer** — incoming only | Smallest change. Unblocks the "agent can see" feature fast. | Asymmetric — agent receives photos but can never send one. Can't generate charts/screenshots and deliver them. |
| **In scope** — agent can send images back | Symmetric. Enables "here's the chart I made" / "here's the screenshot of your calendar." | Each channel's send path needs implementing (Telegram `sendPhoto`, Discord attachment upload, WhatsApp media upload). Doubles the surface area. Many more failure modes. |

**Recommendation: Defer (incoming only in this change).** Keep the change focused. Outgoing is a clean follow-up once the content-block model is battle-tested on the receive path. The core type refactor is the same either direction, so a future outgoing change is purely channel-layer work.

### Q3. Filter layer behavior for non-text content

| Option | Pros | Cons |
|--------|------|------|
| **Passthrough non-text; filter only text blocks** | Preserves filter semantics on text. Raw bytes untouched. Simple. | Filter can't redact a sensitive image. A "redact emails" rule wouldn't catch text baked into a screenshot. |
| **Skip filter entirely if any block is non-text** | Safer — filter never sees anything it can't reason about. | Loses filtering on the text blocks that coexist with the image. Over-cautious. |
| **Per-block filter decisions** | Most precise — per-type filter policies. | Complex. New config surface. Test matrix balloons. |

**Recommendation: Passthrough non-text; filter only text blocks.** This matches the filter's existing semantics (it operates on text) and keeps the change minimal. Image content OCR / scanning is a future concern — bring it up when we have a real use case, not preemptively. Document as a known limitation.

### Q4. Conversation JSON migration

| Option | What it is | Pros | Cons |
|--------|-----------|------|------|
| **Lazy (read-time shim)** | `UnmarshalJSON` on `ChatMessage` accepts both `"content":"hello"` and `"content":[{...}]` | Zero-downtime. No startup cost. Old records just work. | Shim lives forever. New writers must emit the new shape so old binaries don't choke (forward-compat is one-way). |
| **One-shot migration on startup** | First run of new binary rewrites every row to the new shape | Clean — only one shape in the DB afterward. Easier reasoning. | Startup cost proportional to history. Must be idempotent + interruptible. Rollback gets hard once rewritten. |
| **Both** | Lazy shim AND a background rewrite pass | Correctness + cleanliness. | Most code. |

**Recommendation: Lazy shim, no startup migration.** micro-claw is a personal single-user binary — `conversations` is small (hundreds to low-thousands of rows) and a read-time shim costs effectively nothing. Adding a one-shot migration adds rollback risk we don't need. If the shape stabilizes and we later want to drop the shim, we can add a one-off migration then. For now: accept both shapes on read, write only the new shape.

## 6. Risks + Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Test churn across channel + provider + agent + store layers | High | Medium | Land the core type change behind a compile error wave; fix tests layer-by-layer in a single sweep. Exploration estimates 15-25 test files; budget accordingly. |
| Stored conversation JSON migration breaks existing users | Medium | High | Lazy `UnmarshalJSON` shim accepts both shapes (Q4). Add a round-trip test loading a fixture of the OLD format to guarantee forward compat. |
| Binary blob growth in SQLite | High (if P1) / Low (if P3) | High | Choose P3 (CAS) per Q1. Enforce `max_attachment_bytes` at channel edge — reject oversized media with a user-facing "file too big" reply. Add a retention GC. |
| Provider capability mismatch silently drops media | Medium | Medium | `SupportsMultimodal()` is the single source of truth. Agent loop checks it and emits the user-facing notice. Log at INFO when degradation fires. |
| Telegram / WhatsApp media download adds latency and new failure modes | Medium | Medium | Download in the channel goroutine before enqueue; timeout + retry; on failure, enqueue the message as text-only with a "(media failed to download)" notice instead of silently dropping. |
| Large attachments multiply memory pressure against the 4-slot semaphore | Medium | Medium | Stream-to-disk via CAS as soon as bytes land. ChatMessage carries only the `Ref` (hash) in memory; bytes are loaded on demand when the provider needs them. Agent loop never holds full blob in memory across the LLM round-trip. |
| Filter can't redact text-in-image content | Low | Low | Documented limitation. Passthrough only text blocks (Q3). Revisit if a real use case appears. |
| Drop-on-full inbox gets worse with multimodal | Medium | Medium | Out of scope for this change, but call it out explicitly in the release notes. The inbox-overflow fix is a required follow-up. |
| Wire format for `[]ContentBlock` drifts between providers | Medium | Medium | Lock the internal shape in design phase. Providers translate; they don't share the type. Unit tests per provider assert the wire shape. |
| Voice note on text-only provider feels broken to users | Low | Medium | Graceful-degradation notice tells the user explicitly: "I can't listen to voice notes with the current model. I saved it for you." Not silent. |

## 7. Phases

Phased so each phase is independently reviewable and each leaves the tree in a compiling + passing state.

1. **Phase 1 — Core types + capability.** Introduce `ContentBlock`, change `ChatMessage.Content` to `[]ContentBlock`, change `IncomingMessage` / `OutgoingMessage` likewise, add `provider.Provider.SupportsMultimodal()`. Add the lazy `UnmarshalJSON` shim for back-compat. Fix compile errors everywhere with minimal text-only semantics (every old site becomes "one text block"). Land the test wave. Tree green.
2. **Phase 2 — Anthropic multimodal + degradation wiring.** Anthropic provider translates image and audio blocks to native wire format. Text-only path for degradation wired into the provider layer. Agent loop emits the one-line user notice. Unit test: multimodal on, multimodal off.
3. **Phase 3 — Telegram channel media download.** Telegram goroutine downloads `.Photo`, `.Voice`, `.Document`, builds `IncomingMessage` with content blocks, enforces `max_attachment_bytes` at the edge. Caption becomes a text block. Test with recorded fixtures.
4. **Phase 4 — Store persistence (CAS, assuming Q1 → P3).** New `media_blobs` table, `MediaStore` interface, CAS read/write API. ChatMessage `Ref` points at hash. Migration of lazy-shim output to store blobs via CAS. Round-trip test.
5. **Phase 5 — Config + size limits + retention.** `multimodal.*` config keys. Channel-edge size check. Background retention job (on startup + configurable interval) that GCs unreferenced blobs older than `retention_days`.
6. **Phase 6 — Conversation JSON migration verification.** Add fixture-based test that loads a pre-multimodal conversations row and round-trips it through the shim without loss. No code change expected — this is proof of Phase 1's shim.
7. **Phase 7 — OpenAI + Gemini multimodal.** Translate content blocks to each provider's wire format. Audio path for Gemini. Per-provider wire-shape unit tests.
8. **Phase 8 — WhatsApp + Discord + CLI channel updates.** WhatsApp media resolve-and-download via Graph API. Discord `m.Attachments` download. CLI `/attach <path>` syntax for smoke tests.
9. **Phase 9 — Filter layer content-block behavior.** Implement the Q3 choice (recommended: passthrough non-text, filter text only). Test that a mixed text+image message still has its text filtered.
10. **Phase 10 — Test strengthening + full CI rehearsal.** Integration tests end-to-end: Telegram fixture in → Anthropic out → stored + retained. Degradation rehearsal with a text-only provider stub. Retention GC rehearsal with clock injection.

## 8. Rollback Plan

Two layers of rollback, cheapest first.

**Layer 1 — Feature flag (cheap, during rollout).** Gate the channel-level media download behind `multimodal.enabled` (default `true` after Phase 3, switchable to `false`). When off, channels revert to the current text-only guard and `IncomingMessage` carries only a single text block. The type system stays migrated, but no media enters the pipeline. This is the fast-revert knob: flip the config, restart the binary, multimodal is gone without a deploy.

**Layer 2 — Full revert (expensive, last resort).** `git revert` the phase merges in reverse order. The risk is the SQLite schema — if CAS (`media_blobs`) shipped, dropping the table is destructive. Mitigation: the migration is additive (new table, new column, new config keys). Reverting the code leaves the table orphaned but harmless. On next forward-fix we can either re-use the existing table or `DROP TABLE media_blobs` in a one-shot migration.

The lazy `UnmarshalJSON` shim must survive rollback — that is, **the shim ships in Phase 1 and only gets removed in a much later change**. As long as the shim is present, any revert to Phase-1-or-later binaries can still read conversations written by any subsequent phase.

**Forbidden rollback action:** do NOT revert past Phase 1 without a full DB backup — pre-Phase-1 binaries cannot read the new JSON shape, so downgrading past the shim will break conversation loading.

## 9. Dependencies

- No new Go modules expected beyond what the Telegram / WhatsApp / Discord libs already offer for file download. Confirm during design phase.
- WhatsApp Graph API access token must already be configured (it is, per existing whatsapp.go).
- Telegram bot library already exposes `GetFileDirectURL` — no library bump.

## 10. Success Criteria

- [ ] User sends a photo to Telegram; agent using Anthropic replies acknowledging what's in the photo.
- [ ] User sends a voice note to Telegram; agent using Gemini replies acknowledging the audio content.
- [ ] User sends a photo to Telegram while configured with a text-only provider (e.g. local Ollama); agent replies "I can't see images with the current model. I saved it for you." and the photo is persisted.
- [ ] User sends an oversized attachment (> `max_attachment_bytes`); agent replies with a size-limit error and does not persist the blob.
- [ ] Old conversation records (pre-multimodal JSON) load cleanly via the lazy shim; round-trip test passes.
- [ ] Retention GC deletes blobs older than `retention_days` that are no longer referenced by any conversation.
- [ ] All existing channels/providers/agent/store tests pass. New multimodal test coverage exists per phase.
- [ ] `provider.SupportsMultimodal()` is implemented by every provider and the agent loop respects it.
- [ ] No external transcription service is called for any audio payload.
