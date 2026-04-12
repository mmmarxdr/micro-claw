# Config Media Specification

## Purpose

Defines the `media` configuration section added to `AgentConfig`, its fields, defaults, validation rules, and the behavior changes driven by `media.enabled`.

## Requirements

### Requirement: MediaConfig Fields and Defaults

`AgentConfig` SHALL include a `Media MediaConfig` field (YAML key: `media`). `MediaConfig` SHALL have the following fields and defaults:

| Field                | Type            | Default               | YAML key                |
|----------------------|-----------------|-----------------------|-------------------------|
| Enabled              | bool            | true                  | enabled                 |
| MaxAttachmentBytes   | int64           | 10485760 (10 MB)      | max_attachment_bytes    |
| MaxMessageBytes      | int64           | 26214400 (25 MB)      | max_message_bytes       |
| RetentionDays        | int             | 30                    | retention_days          |
| CleanupInterval      | time.Duration   | 24h                   | cleanup_interval        |
| AllowedMIMEPrefixes  | []string        | [image/, audio/, application/pdf, text/] | allowed_mime_prefixes |

When a config file omits the `media` section entirely, all defaults SHALL apply.

#### Scenario: Defaults applied when media section is absent

- GIVEN a config file with no `media` key
- WHEN the config is loaded
- THEN `Media.Enabled == true`, `Media.MaxAttachmentBytes == 10485760`, `Media.RetentionDays == 30`
- AND `Media.AllowedMIMEPrefixes` contains `"image/"`, `"audio/"`, `"application/pdf"`, `"text/"`

---

### Requirement: MaxAttachmentBytes Validation

`MaxAttachmentBytes` MUST be in the range [1024, 52428800] (1 KB to 50 MB). Values outside this range SHALL produce a validation error on config load.

#### Scenario: MaxAttachmentBytes below 1 KB fails

- GIVEN `max_attachment_bytes: 512`
- WHEN the config is validated
- THEN an error is returned referencing `max_attachment_bytes`

---

### Requirement: MaxMessageBytes >= MaxAttachmentBytes

`MaxMessageBytes` SHALL be greater than or equal to `MaxAttachmentBytes`. A config where `MaxMessageBytes < MaxAttachmentBytes` SHALL fail validation.

#### Scenario: max_attachment_bytes > max_message_bytes returns validation error

- GIVEN `max_attachment_bytes: 20971520` and `max_message_bytes: 10485760`
- WHEN the config is validated
- THEN an error is returned stating that `max_message_bytes` must be >= `max_attachment_bytes`

---

### Requirement: RetentionDays Validation

`RetentionDays` MUST be >= 1. A value of 0 or negative SHALL produce a validation error.

#### Scenario: retention_days < 1 returns validation error

- GIVEN `retention_days: 0`
- WHEN the config is validated
- THEN an error is returned referencing `retention_days`

---

### Requirement: AllowedMIMEPrefixes Non-Empty When Enabled

When `Enabled == true` and `AllowedMIMEPrefixes` is explicitly set to an empty list, the config SHALL fail validation with a message indicating the whitelist is empty and directing the user to set `enabled: false` to disable media explicitly.

#### Scenario: Empty whitelist with enabled=true fails

- GIVEN `media.enabled: true` and `media.allowed_mime_prefixes: []`
- WHEN the config is validated
- THEN an error is returned stating the whitelist is empty
- AND the error message references `allowed_mime_prefixes`

---

### Requirement: media.enabled=false Disables Download

When `Media.Enabled == false`, Telegram (and all channels) SHALL skip media download entirely for any update containing non-text content. The user SHALL receive a notice: `"(media ignored — disabled in config)"`. Text content in the same update SHALL still be processed normally.

#### Scenario: Photo received with media.enabled=false

- GIVEN `media.enabled: false`
- AND an incoming Telegram photo update with caption `"look"`
- WHEN processed
- THEN no download is initiated
- AND the enqueued `IncomingMessage.Content` contains `[BlockText{"look"}, BlockText{"(media ignored — disabled in config)"}]`

#### Scenario: Text-only message unaffected by media.enabled=false

- GIVEN `media.enabled: false`
- AND an incoming Telegram text-only update
- WHEN processed
- THEN the message is enqueued normally with no additional notice
