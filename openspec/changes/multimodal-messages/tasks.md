# Tasks: Multimodal Messages

> **HARD ORDERING CONSTRAINT**: Phase 0 MUST merge to main BEFORE any phase that causes
> `ChatMessage.Content` to be serialized as a JSON array. Phases 1–13 depend on Phase 0.

---

## Phase 0 — Forward-Compat Reader (SHIP FIRST, ISOLATED)

- [x] 0.1 Add `ChatMessage.UnmarshalJSON` to `internal/provider/provider.go`: accept both `"content":"string"` (legacy) and `"content":[...]` (new array) forms; on legacy string wrap it in `Blocks{{Type: BlockText, Text: s}}`; on array form unmarshal directly into `content.Blocks`; tool_calls/tool_call_id preserved in both paths (forward-compat-reader, Scenario: Old binary reads new array content)
- [x] 0.2 Add checked-in fixture `internal/provider/testdata/legacy_chatmessage.json` with `{"role":"user","content":"hello from before multimodal"}` (forward-compat-reader, Scenario: Old binary reads legacy string content)
- [x] 0.3 Add table-driven tests in `internal/provider/provider_test.go` (or new `chatmessage_shim_test.go`): legacy string → correct Blocks; array form → correct Blocks; legacy load + re-marshal → array JSON; multi-block text join → TextOnly contains newline (forward-compat-reader, all four scenarios)
- [x] 0.4 Checkpoint: `go vet ./internal/provider/... ./internal/store/...` + `golangci-lint run ./internal/provider/...` + `go test ./internal/provider/...`

> **Gate**: do NOT merge any subsequent phase until Phase 0 passes CI on main.

---

## Phase 1 — Core Types + Content Package

- [x] 1.1 Create `internal/content/block.go`: define `BlockType` string type; constants `BlockText`, `BlockImage`, `BlockAudio`, `BlockDocument`; `ContentBlock` struct with fields `Type`, `Text`, `MediaSHA256`, `MIME`, `Size int64`, `Filename`; `Blocks []ContentBlock` type alias (content-types, Requirement: BlockType Constants)
- [x] 1.2 Add helpers to `internal/content/block.go`: `(bs Blocks) TextOnly() string` (joins text blocks with `\n`, skips non-text); `(bs Blocks) HasMedia() bool` (any non-text); `TextBlock(s string) Blocks` constructor (content-types, Requirement: Blocks.TextOnly Helper)
- [x] 1.3 Add `content.UnmarshalBlocks(raw json.RawMessage) (Blocks, error)` to `internal/content/block.go`: nil/null/empty → `(nil, nil)`; first byte `"` → legacy string path → `TextBlock(s)`; else → array unmarshal (content-types, Requirement: UnmarshalBlocks Shared Shim)
- [x] 1.4 Create `internal/content/degrade.go`: `FlattenBlocks(bs Blocks) string` produces placeholder text for each non-text block: `[<type> attached: <filename|type>, <human-readable size>, MIME <mime>, not processed by current model]`; `DegradationNotice(bs Blocks) string` returns image or audio notice string (provider-capability, Requirement: Degradation Notice; design §3)
- [x] 1.5 Replace `IncomingMessage.Text string` with `Content content.Blocks` in `internal/channel/channel.go`; add `func (m IncomingMessage) Text() string { return m.Content.TextOnly() }` compatibility method (content-types, Requirement: IncomingMessage Content Field)
- [x] 1.6 Replace `ChatMessage.Content string` with `Content content.Blocks` in `internal/provider/provider.go`; remove the Phase 0 `UnmarshalJSON` and replace with the production version (now with `content.UnmarshalBlocks`); no custom `MarshalJSON` — default struct marshal writes array form (content-types, Requirement: ChatMessage Content Field)
- [x] 1.7 Update `ChatResponse.Content string` call-sites in `internal/provider/provider.go` — response stays `string` (no change needed; outgoing text-only is deferred per D3)
- [x] 1.8 Update all `msg.Text` / `msg.Content` call-sites per design §6.2 inventory: `loop.go` lines for `len(msg.Text)`, `Content: msg.Text`, `SearchMemory(..., msg.Text)`, `agent.go:truncate(m.Text)`, and any test fixtures using `Text:` assignment → replace with `Content.TextOnly()` or `content.TextBlock(...)` (content-types, design §6.2)
- [x] 1.9 Create `internal/content/block_test.go`: table tests for `TextOnly` (empty, all-text, mixed, all-media), `HasMedia`, `UnmarshalBlocks` (null, legacy string, array), `TextBlock`, `ContentBlock` JSON round-trip for text and image blocks (content-types, all Scenarios)
- [x] 1.10 Checkpoint: `go vet ./...` + `golangci-lint run ./...` + `go test ./internal/content/... ./internal/provider/... ./internal/channel/... ./internal/agent/...`

---

## Phase 2 — Provider Capability + Graceful Degradation

- [x] 2.1 Add `SupportsMultimodal() bool` and `SupportsAudio() bool` to the `Provider` interface in `internal/provider/provider.go` (provider-capability, Requirement: Provider Interface Capability Methods)
- [x] 2.2 Implement capability methods on each provider — Anthropic (`true`/`false`), OpenAI (`true`/`true`), Gemini (`true`/`true`), OpenRouter (`true`/`true`) in their respective `*.go` files (provider-capability, Requirement: Capability Matrix)
- [x] 2.3 Implement `SupportsMultimodal` and `SupportsAudio` on the Fallback provider in `internal/provider/fallback.go`: AND over all pool members; empty pool MUST return `false` (not vacuous true) — add nil/empty guard (provider-capability, Requirement: Fallback Provider Reports Logical AND; spec risk 1)
- [x] 2.4 Add degradation check in `internal/agent/loop.go` immediately before `provider.Chat()` call: check `!provider.SupportsMultimodal() && lastUserMsg.Content.HasMedia()`; set `degraded bool`; after provider returns prepend `content.DegradationNotice(blocks)` to reply; emit `slog.Info("degradation", "provider_name", ..., "block_types", ...)` (agent-loop-multimodal, Requirement: Degradation Check; Degradation Logging)
- [x] 2.5 Add unit tests in `internal/provider/fallback_test.go`: AND-over-mixed-pool returns false; all-multimodal pool returns true; empty pool returns false for both methods (provider-capability, Requirement: Fallback; spec risk 1)
- [x] 2.6 Add degradation unit tests in `internal/agent/loop_test.go`: image block + text-only mock provider → reply prefixed with notice; image block + multimodal mock provider → no notice; text-only message + text-only provider → no notice (agent-loop-multimodal, all degradation Scenarios)
- [x] 2.7 Checkpoint: `go vet ./internal/provider/... ./internal/agent/...` + `golangci-lint run` + `go test -race ./internal/provider/... ./internal/agent/...`

---

## Phase 3 — Anthropic Multimodal Translation

- [x] 3.1 Update Anthropic request builder in `internal/provider/anthropic.go` (and `anthropic_stream.go`): translate each `ContentBlock` → Anthropic native format; `BlockText` → `{"type":"text","text":"..."}` part; `BlockImage` → `{"type":"image","source":{"type":"base64","media_type":"<mime>","data":"<b64>"}}`; load bytes via `store.GetMedia(sha256)` (provider-capability, Requirement: Multimodal Provider Translates Image Blocks)
- [x] 3.2 Add token estimate constant `AnthropicImageTokens = 1500` in `internal/agent/tokens.go` (or `internal/provider/anthropic.go`) with a source comment: `// ~1500 tokens per image per Anthropic docs (https://docs.anthropic.com/en/docs/build-with-claude/vision#image-costs)`; wire into context builder's per-block estimate (agent-loop-multimodal, Requirement: context.go Iterates Blocks; spec risk 2)
- [x] 3.3 Add golden-file test `internal/provider/testdata/anthropic_multimodal_request.json` and test in `internal/provider/anthropic_test.go` asserting the exact wire shape for a message with one text block and one image block (provider-capability, Scenario: Anthropic translates image block)
- [x] 3.4 Checkpoint: `go vet ./internal/provider/... ./internal/agent/...` + `golangci-lint run` + `go test ./internal/provider/... ./internal/agent/...`

---

## Phase 4 — CAS Media Store

- [x] 4.1 Create `internal/store/media.go`: define `MediaStore` interface with methods `StoreMedia`, `GetMedia`, `TouchMedia`, `PruneUnreferencedMedia`; define sentinel `ErrMediaNotFound` error; define `ErrMediaNotSupported` error for FileStore (media-store, Requirement: MediaStore Interface)
- [x] 4.2 Add `migrateV5()` in `internal/store/migration.go`: `CREATE TABLE IF NOT EXISTS media_blobs (sha256 TEXT PRIMARY KEY, mime TEXT NOT NULL, size INTEGER NOT NULL, data BLOB NOT NULL, created_at TEXT NOT NULL, last_referenced_at TEXT NOT NULL); CREATE INDEX IF NOT EXISTS idx_media_last_referenced ON media_blobs(last_referenced_at);`; call from `initSchema` after v4 (media-store, Requirement: media_blobs Schema)
- [x] 4.3 Implement `StoreMedia` on `*SQLiteStore` in `internal/store/sqlitestore.go`: SHA256 hash input bytes; `INSERT OR IGNORE INTO media_blobs`; return hex sha256 (media-store, Requirement: StoreMedia Returns SHA256 and Deduplicates)
- [x] 4.4 Implement `GetMedia` on `*SQLiteStore`: SELECT by sha256; return `ErrMediaNotFound` for unknown sha256 (media-store, Requirement: GetMedia Returns Bytes and MIME)
- [x] 4.5 Implement `TouchMedia` on `*SQLiteStore`: UPDATE `last_referenced_at = NOW()` WHERE sha256; return `ErrMediaNotFound` if 0 rows affected (media-store, Requirement: TouchMedia Updates last_referenced_at)
- [x] 4.6 Implement `PruneUnreferencedMedia` on `*SQLiteStore`: DELETE WHERE `last_referenced_at < threshold AND sha256 NOT IN (json scan of conversations)`; return deleted row count (media-store, Requirement: PruneUnreferencedMedia Deletes Stale Blobs)
- [x] 4.7 Wire `TouchMedia` calls in `SaveConversation` and `LoadConversation` on `*SQLiteStore`: walk all content blocks, collect sha256s, batch UPDATE `last_referenced_at` (design §4.4)
- [x] 4.8 Add startup warning log in `internal/agent/agent.go` or `internal/store/filestore.go` when FileStore is the backing store and `media.enabled=true`: log warning, set mediaStore to nil, channels degrade to text-only (media-store, Requirement: FileStore Does Not Implement MediaStore)
- [x] 4.9 Create `internal/store/media_test.go`: StoreMedia returns sha256; dedup (2x same bytes = 1 row, same sha256); GetMedia byte-for-byte match; GetMedia unknown → ErrMediaNotFound; TouchMedia updates timestamp; TouchMedia unknown → ErrMediaNotFound; PruneUnreferencedMedia: 3 blobs (1 stale unreferenced, 1 fresh, 1 stale but referenced) → only stale unreferenced deleted, count=1 (media-store, all Scenarios)
- [x] 4.10 Checkpoint: `go vet ./internal/store/...` + `golangci-lint run ./internal/store/...` + `go test ./internal/store/...`

---

## Phase 5 — Config Schema + Validation

- [x] 5.1 Add `MediaConfig` struct to `internal/config/config.go`: fields `Enabled bool`, `MaxAttachmentBytes int64`, `MaxMessageBytes int64`, `RetentionDays int`, `CleanupInterval time.Duration`, `AllowedMIMEPrefixes []string` with YAML tags; add `Media MediaConfig` field to `AgentConfig` (config-media, Requirement: MediaConfig Fields and Defaults)
- [x] 5.2 Add defaults in `applyDefaults` in `config.go`: `Enabled=true`, `MaxAttachmentBytes=10485760`, `MaxMessageBytes=26214400`, `RetentionDays=30`, `CleanupInterval=24h`, `AllowedMIMEPrefixes=["image/","audio/","application/pdf","text/"]` (config-media, Requirement: MediaConfig Fields and Defaults)
- [x] 5.3 Add validation in `validate()` in `config.go`: `MaxAttachmentBytes` in [1024, 52428800]; `MaxMessageBytes >= MaxAttachmentBytes`; `RetentionDays >= 1`; `CleanupInterval >= 1h`; `Enabled && len(AllowedMIMEPrefixes)==0` → error referencing `allowed_mime_prefixes` (config-media, all validation Requirements)
- [x] 5.4 Add config tests in `internal/config/` (extend `context_mode_test.go` or new file): defaults applied when section absent; MaxAttachmentBytes < 1KB fails; MaxMessageBytes < MaxAttachmentBytes fails; retention_days=0 fails; empty whitelist + enabled=true fails (config-media, all Scenarios)
- [x] 5.5 Checkpoint: `go vet ./internal/config/...` + `golangci-lint run ./internal/config/...` + `go test ./internal/config/...`

---

## Phase 6 — Telegram Channel Media Download

- [x] 6.1 Add media config guard at top of Telegram update handler in `internal/channel/telegram.go`: if `media.enabled=false` and update has media, enqueue text from caption + `BlockText{"(media ignored — disabled in config)"}`, return early (config-media, Requirement: media.enabled=false Disables Download)
- [x] 6.2 Add pre-download size gate in Telegram handler: for photos use last element `Photo[len-1].FileSize`; if `FileSize > max_attachment_bytes`, enqueue `BlockText{"(attachment too large: <size> exceeds limit <limit>)"}`, return; also check total message bytes gate (telegram-channel-media, Requirements: Pre-Download Size Gate, Total Message Bytes Gate)
- [x] 6.3 Implement photo download: `bot.GetFileDirectURL(fileID)` + `http.Get` with 30s timeout; on success: `store.StoreMedia(bytes, "image/jpeg")`; build `[BlockText{caption}, BlockImage{sha, mime, size}]` (caption omitted if empty); on CDN failure: `[BlockText{caption}, BlockText{"(media failed to download: <reason>)"}]` (telegram-channel-media, Requirements: Photo Download and Block Construction, CDN Download Failure)
- [x] 6.4 Implement voice note download in Telegram handler: MIME `audio/ogg`; download + store; build `[BlockAudio{sha, "audio/ogg", size}]` (telegram-channel-media, Requirement: Voice Note Download and Block Construction)
- [x] 6.5 Implement document download in Telegram handler: check MIME against `allowed_mime_prefixes` before download; if rejected: `BlockText{"(attachment type not allowed: <mime>)"}`; if allowed: download + store + `BlockDocument{sha, mime, size, filename}` (telegram-channel-media, Requirements: Document Download, MIME Whitelist Enforcement)
- [x] 6.6 Add Telegram channel tests in `internal/channel/telegram_test.go`: photo with caption → `[text, image]` blocks; photo without caption → `[image]`; photo oversized → rejection notice, no download; CDN 500 with caption → `[text, notice]`; CDN timeout with no caption → `[notice]`; blocked MIME → rejection notice; media.enabled=false → text + disabled notice (telegram-channel-media, all Scenarios)
- [x] 6.7 Checkpoint: `go vet ./internal/channel/...` + `golangci-lint run ./internal/channel/...` + `go test ./internal/channel/...`

---

## Phase 7 — Agent Loop + Context Builder Multimodal Flow

- [x] 7.1 Update `processMessage` in `internal/agent/loop.go`: replace `Content: msg.Text` with `Content: msg.Content`; add `slog.Debug` log with `block_count`, `text_len`, `has_media` fields (agent-loop-multimodal, Requirement: IncomingMessage Content Appended to Conversation)
- [x] 7.2 Update `internal/agent/context.go` context builder to iterate `ChatMessage.Content` block by block: text blocks use character/token count as before; image/audio/document blocks use per-provider constant estimates (`AnthropicImageTokens`, `OpenAIImageTokens`, `GeminiImageTokens`); truncation drops whole `ChatMessage`, never individual blocks (agent-loop-multimodal, Requirement: context.go Iterates Blocks)
- [x] 7.3 Add `OpenAIImageTokens = 85` (baseline tile cost, actual is 85+tiles) and `GeminiImageTokens = 258` to `internal/agent/tokens.go` with source comments: OpenAI: `// baseline 85 tokens + variable tiles per https://platform.openai.com/docs/guides/vision/calculating-costs`; Gemini: `// 258 tokens per image per Gemini docs` (spec risk 2)
- [x] 7.4 Update loop_test.go fixtures: replace any `msg.Text = "..."` or `Content: "..."` on ChatMessage with `content.TextBlock(...)` construction; verify tests still pass (design §6.2)
- [x] 7.5 Checkpoint: `go vet ./internal/agent/...` + `golangci-lint run ./internal/agent/...` + `go test -race ./internal/agent/...`

---

## Phase 8 — OpenAI + Gemini Multimodal Translation

- [x] 8.1 Update OpenAI request builder in `internal/provider/openai.go` (and `openai_stream.go`): translate `BlockImage` → `{"type":"image_url","image_url":{"url":"data:<mime>;base64,<b64>"}}`; load bytes via `store.GetMedia` (provider-capability, Scenario: OpenAI translates image block)
- [x] 8.2 Update Gemini request builder in `internal/provider/gemini.go` (and `gemini_stream.go`): translate `BlockImage` → `{"inlineData":{"mimeType":"<mime>","data":"<b64>"}}`; translate `BlockAudio` similarly; load bytes via `store.GetMedia` (provider-capability, Scenario: Gemini translates image block)
- [x] 8.3 Add golden-file test `internal/provider/testdata/openai_multimodal_request.json` + test in `openai_test.go` asserting `image_url` wire shape (provider-capability, Scenario: OpenAI)
- [x] 8.4 Add golden-file test `internal/provider/testdata/gemini_multimodal_request.json` + test in `gemini_test.go` asserting `inlineData` wire shape and audio block handling (provider-capability, Scenario: Gemini)
- [x] 8.5 Checkpoint: `go vet ./internal/provider/...` + `golangci-lint run ./internal/provider/...` + `go test ./internal/provider/...`

---

## Phase 9 — CLI + Discord + WhatsApp Channel Updates

- [x] 9.1 Add `/attach <path>` command parsing in `internal/channel/cli.go`: `os.ReadFile`, `http.DetectContentType`, `store.StoreMedia`, build `ContentBlock`; size gate against `max_attachment_bytes`; MIME check against `allowed_mime_prefixes` (proposal Phase 8, design §5.4)
- [x] 9.2 Update Discord handler in `internal/channel/discord.go`: iterate `m.Attachments`; size gate; download via direct URL with 30s timeout; MIME check; `store.StoreMedia`; build blocks; CDN failure → text notice (design §5.3)
- [x] 9.3 Update WhatsApp handler in `internal/channel/whatsapp.go`: resolve `media_id` via Graph API `GET /{media_id}` to get download URL; download bytes; size gate; MIME check; `store.StoreMedia`; build blocks (design §5.2)
- [x] 9.4 Add CLI test in `internal/channel/cli_test.go`: `/attach` with a temp file → content block present; oversized file → rejection notice; blocked MIME → rejection notice (proposal Phase 8)
- [x] 9.5 Checkpoint: `go vet ./internal/channel/...` + `golangci-lint run ./internal/channel/...` + `go test ./internal/channel/...`

---

## Phase 10 — Filter Layer Regression Guard

- [x] 10.1 Add `// TODO(multimodal-tool-output): filter operates on text tool output only; content blocks live in context.go` comment anchor to `internal/filter/filter.go` near the `Apply` function signature — ZERO other changes to filter.go (design §7, Q3 decision)
- [x] 10.2 Add regression test in `internal/filter/filter_test.go`: verify `PreApply` + `Apply` compile and work unchanged with a text-only `ToolResult`; assert filter function signatures are unchanged (design §7)
- [x] 10.3 Checkpoint: `go vet ./internal/filter/...` + `golangci-lint run ./internal/filter/...` + `go test ./internal/filter/...`

---

## Phase 11 — Media Cleanup Scheduler

- [x] 11.1 Add `mediaCleanupLoop(ctx context.Context)` method to `Agent` in `internal/agent/agent.go`: `time.NewTicker(cfg.Media.CleanupInterval)`; on tick call `store.(MediaStore).PruneUnreferencedMedia(ctx, duration)`; log deleted count at INFO; cancel on `ctx.Done()` (design §8, proposal Phase 5)
- [x] 11.2 Wire `mediaCleanupLoop` in `Agent.Run()`: launch as goroutine only when `cfg.Media.Enabled && store implements MediaStore`; stop on ctx cancel (design §8)
- [x] 11.3 Add clock-injectable test in `internal/agent/` (or extend `integration_test.go`): fake ticker fires → `PruneUnreferencedMedia` called with expected duration; context cancel → loop exits cleanly (proposal Phase 5)
- [x] 11.4 Checkpoint: `go vet ./internal/agent/...` + `golangci-lint run ./internal/agent/...` + `go test -race ./internal/agent/...`

---

## Phase 12 — Integration Tests

- [x] 12.1 Add integration test: mock Telegram update with photo → agent loop with mock Anthropic multimodal provider → assert provider receives `BlockImage` in request body (design §10, row: Integration Telegram → Anthropic mock)
- [x] 12.2 Add integration test: mock Telegram update with photo → agent loop with mock text-only provider (Ollama-style) → assert provider receives flattened placeholder text + reply is prefixed with degradation notice (design §10, row: degradation rehearsal)
- [x] 12.3 Add integration test: mock Telegram update with voice note → agent loop with mock Gemini provider (audio=true) → assert audio block present in provider request (design §10, row: voice note)
- [x] 12.4 Add integration test: mock Telegram update with oversized photo → assert no download attempt + rejection notice in reply (telegram-channel-media, Requirement: Pre-Download Size Gate)
- [x] 12.5 Add legacy fixture round-trip integration test in `internal/agent/integration_test.go`: load `testdata/legacy_chatmessage.json` → unmarshal → re-marshal → unmarshal again → all content preserved; never delete this fixture (forward-compat-reader, Scenario: Load legacy, re-save writes new array form)
- [x] 12.6 Add retention GC integration test with fake clock: insert 3 blobs (1 stale+unreferenced, 1 fresh, 1 stale+referenced in conv) → PruneUnreferencedMedia → 1 deleted (media-store, PruneUnreferencedMedia scenarios)
- [x] 12.7 Checkpoint: `go vet ./...` + `golangci-lint run ./...` + `go test -race -timeout 300s ./...`

---

## Phase 13 — Full CI Rehearsal

- [x] 13.1 `go build ./...` — zero errors
- [x] 13.2 `go vet ./...` — zero warnings
- [x] 13.3 `go test -race -timeout 300s -count=1 ./...` — all tests pass with race detector
- [x] 13.4 `golangci-lint run ./...` — zero lint issues
- [x] 13.5 Document binary size baseline vs new in a code comment in `cmd/` or as a CI step note (pre: baseline, post: expected delta from media_blobs + content package)
- [x] 13.6 Document manual smoke-test procedure for Telegram with real API key in `openspec/changes/multimodal-messages/smoke-test.md` (optional but recommended before release)
