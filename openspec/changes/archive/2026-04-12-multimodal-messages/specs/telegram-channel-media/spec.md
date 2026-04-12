# Telegram Channel Media Specification

## Purpose

Defines how the Telegram channel goroutine downloads and builds `ContentBlock` slices from photo, voice, and document updates. Covers size enforcement, MIME detection, caption handling, and graceful degradation on download failure.

## Requirements

### Requirement: Pre-Download Size Gate

Before initiating any network download, the Telegram channel SHALL reject any attachment whose declared file size exceeds `media.max_attachment_bytes`. For photos, the last (largest) element in the `Photo` slice is the reference size. The rejection MUST NOT initiate a download. The user SHALL receive a notice.

#### Scenario: Photo exceeding max_attachment_bytes rejected before download

- GIVEN an incoming Telegram update with a photo whose declared `FileSize > max_attachment_bytes`
- WHEN the channel goroutine processes the update
- THEN no download request is made
- AND the message is enqueued as a text block: `"(attachment too large: <size> exceeds limit <limit>)"`

#### Scenario: Photo within limit proceeds to download

- GIVEN an incoming Telegram update with a photo whose declared `FileSize <= max_attachment_bytes`
- WHEN the channel goroutine processes the update
- THEN a download is initiated

---

### Requirement: Total Message Bytes Gate

If the sum of all attachment sizes in a single update would exceed `media.max_message_bytes`, the entire media payload SHALL be rejected before any download. The user SHALL receive a notice.

#### Scenario: Total size exceeds max_message_bytes

- GIVEN an update with multiple attachments whose combined declared size > `max_message_bytes`
- WHEN processed
- THEN no downloads are initiated
- AND the user receives a size-limit rejection notice

---

### Requirement: Photo Download and Block Construction

For photos within size limits, the channel SHALL download via `bot.GetFileDirectURL(fileID)` + HTTP GET, store via `StoreMedia`, and build a `BlockImage` content block. The caption (if present) SHALL be prepended as a `BlockText` content block.

#### Scenario: Photo with caption builds [text, image] blocks

- GIVEN an incoming photo update with caption `"check this out"`
- WHEN successfully downloaded and stored
- THEN `IncomingMessage.Content` is `[BlockText{"check this out"}, BlockImage{sha256, "image/jpeg", size}]`

#### Scenario: Photo without caption builds [image] block only

- GIVEN an incoming photo update with no caption
- WHEN successfully downloaded and stored
- THEN `IncomingMessage.Content` is `[BlockImage{sha256, mime, size}]`

---

### Requirement: Voice Note Download and Block Construction

Voice notes (OGG Opus) SHALL be downloaded and stored as `BlockAudio` with MIME `audio/ogg`.

#### Scenario: Voice note builds [audio] block

- GIVEN an incoming Telegram voice note update
- WHEN downloaded and stored
- THEN `IncomingMessage.Content` is `[BlockAudio{sha256, "audio/ogg", size}]`

---

### Requirement: Document Download and Block Construction

Documents passing the MIME whitelist check SHALL be downloaded and stored as `BlockDocument`, preserving `FileName` from the Telegram document object.

#### Scenario: Document builds [document] block with filename

- GIVEN an incoming Telegram document update with `FileName = "invoice.pdf"` and MIME `application/pdf`
- AND `application/pdf` matches an entry in `media.allowed_mime_prefixes`
- WHEN downloaded and stored
- THEN `IncomingMessage.Content` is `[BlockDocument{sha256, "application/pdf", size, filename: "invoice.pdf"}]`

---

### Requirement: MIME Whitelist Enforcement

Before downloading any document (not photo/voice), the channel SHALL check the declared MIME against `media.allowed_mime_prefixes`. A MIME that does not match any prefix SHALL result in rejection without download. The user SHALL receive a notice.

#### Scenario: MIME outside allowed_mime_prefixes rejected

- GIVEN a document with MIME `application/x-executable`
- AND `allowed_mime_prefixes` does not include `application/x-executable` or `application/`
- WHEN processed
- THEN no download is made
- AND the user receives `"(attachment type not allowed: application/x-executable)"`

---

### Requirement: CDN Download Failure — Media Dropped, Text Kept

If the HTTP download of an attachment fails (non-2xx response, timeout, or network error), the media block SHALL be dropped. Any accompanying caption SHALL be preserved as a text block. A notice SHALL be appended.

#### Scenario: CDN download fails — caption preserved, notice added

- GIVEN an incoming photo with caption `"look at this"` and a CDN that returns 500
- WHEN the channel processes the update
- THEN `IncomingMessage.Content` contains a `BlockText{"look at this"}` block
- AND an additional `BlockText{"(media failed to download: <reason>)"}` block is appended
- AND no image block is present

#### Scenario: CDN download fails — no caption

- GIVEN an incoming photo with no caption and a CDN that returns a timeout error
- WHEN processed
- THEN `IncomingMessage.Content` is `[BlockText{"(media failed to download: <reason>)"}]`
