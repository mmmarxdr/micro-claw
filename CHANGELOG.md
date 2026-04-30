# Changelog

All notable changes to Daimon are documented here.

Releases follow [semver](https://semver.org). Pre-1.0 minors may break configuration; patch releases never do.

---

## [v0.10.1] — Frontend catch-up + audit/config race fixes

**Release date**: 2026-04-30

A patch release with two motivations:

1. **The v0.10.0 release shipped the backend endpoints documented below
   (system pulse, audit hot-swap, sidebar telemetry, audit-from-Settings)
   but the embedded frontend bundle was still v0.8.0 and could not
   consume them.** Users running `daimon update` saw a stale UI. v0.10.1
   ships frontend v0.9.0 so the v0.10.0 UI is finally visible.
2. Two CRITICAL data races surfaced by an internal review on v0.10.0
   are closed.

### Fixed

- **Audit hot-swap data race.** The agent and `/ws/logs` held the
  previous auditor after `PUT /api/config` swapped the backend, so a
  `Close()` on the old one could fire concurrent with reads. The agent
  now resolves the auditor through an accessor callback (`auditorFn`)
  read under `auditorMu.RLock` on every `Emit`. WS logs re-resolves via
  `s.CurrentAuditor()` on each 2s poll tick.
- **Config snapshot torn-read.** `*s.deps.Config = merged` is a
  non-atomic struct assignment; readers without a lock could observe
  partially updated state. `configMu` is now `sync.RWMutex` and every
  reader takes a snapshot via `s.config()`. `handlePutConfig` releases
  the write lock before `rebuildAuditor` and `handleGetConfig` to avoid
  the non-reentrant RWMutex deadlock.
- **Embedded frontend bundle** updated to daimon-frontend v0.9.0. The
  v0.10.0 features that shipped backend-only — sidebar telemetry,
  system pulse panel, audit toggle in Settings, LogsPage/ToolsPage
  Liminal redesigns, empty states, sidebar version footer — are now
  reachable from the dashboard.

### New

- **Chat dock mini-player** (`daimon-frontend` PR #2 by
  `@mauroasoriano22`). When you navigate away from `/chat`, the chat
  compresses into a small dock anchored to the bottom-right corner.
  Click to expand back to fullscreen; X to dismiss. Dismissal persists
  in `localStorage` until you revisit `/chat`. The WebSocket and turn
  state stay alive across navigation, so a long-running turn is no
  longer killed when you peek at Memory or Settings.

---

## [v0.10.0] — Audit hot-swap, system pulse, full Liminal coverage

**Release date**: 2026-04-25

A polish release focused on UX completeness ahead of the 1.0 milestone:
the dashboard now covers every page in the Liminal aesthetic, surfaces
process and host telemetry, and audit logging works out of the box —
no hand-edited YAML required.

### New

- **System pulse on Metrics** — process RSS, host CPU/memory/disk, and a
  per-subsystem storage breakdown (store / audit / skills) with bar fills
  that turn amber and red at configurable thresholds. Refreshes every 5s.
- **Audit logging from Settings** — toggle audit on/off, pick the backend
  (sqlite for streaming, file for append-only), and choose a path. Changes
  hot-swap the running auditor without a restart; `/logs` picks up the new
  stream on the next connection.
- **LogsPage** redesigned in Liminal with level filters, pulsing connection
  dot, and a banner that explains *why* streaming is unavailable when audit
  is off or pointed at a non-streaming backend.
- **ToolsPage** redesigned in Liminal — last page on the migration list.
- **Empty states** — Conversations, Memory, and Knowledge greet first-run
  users with editorial copy and a clear CTA instead of blank screens.
- **Version visible** in the sidebar footer.

### Changed

- `audit.enabled` now defaults to `true` and `audit.type` defaults to
  `sqlite`. The setup wizard provisions sqlite. Existing configs with an
  explicit `enabled: false` are honored — the *bool migration preserves
  opt-out.
- `PUT /api/config` accepts the `audit` subtree (previously dropped).
  When any audit field is patched, the server hot-swaps the auditor.
- Conversations page strings translated to English (search placeholder,
  loading, error, no-match, pagination, delete confirm).

### Fixed

- `/ws/logs` no longer renders the "audit backend does not support
  RecentEvents" status frame as a regular log line — it carries
  `event_type=stream_unavailable` and surfaces as a banner with an
  actionable CTA.

### Internals

- `currentAuditor()` / `rebuildAuditor()` — read under RWMutex.RLock for
  wait-free WS handshakes; rebuild closes the old backend and atomically
  installs a new one.
- `audit.LogStreamer` interface remains the contract; only
  `SQLiteAuditor` implements it. FileAuditor and NoopAuditor degrade
  gracefully through the WS handler with distinct messages.
- `gopsutil/v3` added as a dependency for process and host metrics.

---

## [v0.9.0] — Conversations resume + PDF inlining

**Release date**: 2026-04-24

### New

- **Conversation resume** — closing the browser no longer ends the
  conversation. Re-opening the dashboard hydrates the last thread on
  `/chat` directly from the server, replacing the previous intermediate
  preview page.
- **Conversation lifecycle** — soft-delete with 30-day retention, a
  background pruner that hard-deletes after the window, an async title
  generator that names threads from their first user turn, and stable
  `conversation_id` propagated end-to-end (config, store, agent loop).
- **Conversations REST endpoints** — paginated list, message window, and
  delete; all auth-gated.

### Fixed

- **PDF and document attachments** — content is now extracted server-side
  and inlined into the user message instead of being silently dropped on
  providers that don't support native documents (the OpenAI shim used by
  OpenRouter, in particular).
- **RAG over-recall on attachment turns** — short user prompts that lean
  on a fresh attachment ("summarize this") used to trigger BM25 against
  unrelated docs; the loop now skips RAG when the message is
  attachment-dominant and the text is short.
- **WebSocket goroutine leak** — `WebChannel.HandleWebSocket`'s ping
  goroutine now exits cleanly when the handler returns.
- **Shell whitelist bypass (RCE)** — `cmd; rm -rf` no longer slips past
  the allow-list check; the executor splits whitelisted vs. raw paths.

---

## [v0.8.0] — RAG precision + self-updating CLI

**Release date**: 2026-04-22

### New

- **`daimon update`** and **`daimon version`** subcommands — the binary
  fetches matching release assets from GitHub, verifies, and atomically
  replaces itself at `os.Executable()`. Flags: `--check`, `--version`.
- **HyDE retrieval** — generates a hypothetical answer with a configurable
  model, embeds it alongside the query, and fuses scores via RRF.
  Configurable timeout, query weight, and candidate cap.
- **Neighbor expansion + score thresholds** — pulls adjacent chunks
  around top hits and discards low-similarity results so the final
  context window stays dense with relevant material.
- **RAG-wide metrics** — counter and timing histograms surfaced via
  `/api/metrics/rag`.
- **Pure-vector search mode** — `SearchOptions.SkipFTS` for clients that
  want only embedding similarity.

### Fixed

- Chunker tail-junk and triple-overlap edge cases.
- Two dead-config bugs surfaced during HyDE testing.
- `web` subcommand wires RAG so `/api/knowledge` works.

---

## [v0.7.0] — Knowledge base + curated memory

**Release date**: 2026-04-21

### New

- **Knowledge base** — endpoints, schema, extractors (PDF, DOCX,
  Markdown, plain text), and a batch ingestion worker. Documents are
  chunked, embedded, and made available to the agent through RAG search.
- **Embedding subsystem** — pluggable provider with a dedicated config
  block; supports batching and a configurable model. OpenAI and Gemini
  backends shipped.
- **Memory clusters** — observations now carry a cluster classification
  (certain / inferred / assumed) surfaced on MemoryPage.
- **Memorable-fact curator** — selects high-signal observations from the
  conversation history and persists them as memory entries.
- **Cross-scope memory access** for the admin user (project + personal).
- **Actionable tool timeout copy** — when a tool exceeds its budget the
  agent surfaces *why* and offers a retry hint instead of a stack trace.
- **Process-group kill on timeout** — child processes spawned by tools
  no longer leak when the wrapper times out.
- **Turn deadline** — total turn time is enforced in addition to
  per-tool timeouts.

---

## [v0.6.0] — Liminal redesign + budget loop + loop detection

**Release date**: 2026-04-20

### New

- **Liminal design system** — typography (serif display, mono data),
  CSS-variable palette, breathing glyph wordmark, and a new layout that
  replaced the previous flat dashboard. ChatPage migrated as the
  reference implementation.
- **Budget-based loop control** — the agent now tracks token spend per
  turn against a configurable budget instead of capping iterations
  blindly, allowing long-but-cheap turns and stopping expensive ones.
- **Loop detection** — recognizes when the same tool call repeats
  unproductively and breaks out with an explanation.
- **`search_output` tool by default** — the agent can grep its own
  recent tool outputs without re-running them.
- **Truncation byte counts** surfaced in tool output so the agent knows
  *how much* was cut, not just *that* it was cut. Shell limit raised
  10K → 64K, HTTP 512K → 2MB.
- **Gemini and Ollama reasoning streaming** — the providers now stream
  thinking tokens alongside content, matching Anthropic's behavior.

---

## [v0.5.0] — Dynamic model discovery + reasoning streaming

**Release date**: 2026-04-19

### New

- **Dynamic model discovery** — providers expose `ListModels()` so the
  dashboard can populate the model picker from the live catalog instead
  of hard-coded lists, and runtime pricing lookups stay correct as
  vendors release new models.
- **Reasoning token streaming** for providers that emit them
  (Anthropic native, OpenRouter compatible).

---

## [v0.4.0] — BREAKING: Product renamed microagent → daimon

**Release date**: 2026-04-19

This release completes the product rename from `microagent` to `daimon`.
It is a breaking change for all users. No backward compatibility is provided.

### Breaking changes

| What changed | Old | New |
|-------------|-----|-----|
| Binary name | `microagent` | `daimon` |
| Config directory | `~/.microagent/` | `~/.daimon/` |
| Database filename | `microagent.db` | `daimon.db` |
| Web token env var | `MICROAGENT_WEB_TOKEN` | `DAIMON_WEB_TOKEN` |
| Jina API key env var | `MICROAGENT_JINA_API_KEY` | `DAIMON_JINA_API_KEY` |
| Secret key env var | `MICROAGENT_SECRET_KEY` | `DAIMON_SECRET_KEY` |
| Go module path | `module microagent` | `module daimon` |
| GitHub repository | `github.com/mmmarxdr/micro-claw` | `github.com/mmmarxdr/daimon` |

### Migration steps (manual — no automatic migration)

1. **Move config directory:**
   ```bash
   mv ~/.microagent ~/.daimon
   ```

2. **Rename the database file:**
   ```bash
   mv ~/.daimon/data/microagent.db ~/.daimon/data/daimon.db
   ```

3. **Update environment variables** in your shell profile or secrets manager:
   - `MICROAGENT_WEB_TOKEN` → `DAIMON_WEB_TOKEN`
   - `MICROAGENT_JINA_API_KEY` → `DAIMON_JINA_API_KEY`
   - `MICROAGENT_SECRET_KEY` → `DAIMON_SECRET_KEY`

4. **Update any systemd service files** or scripts that reference the old
   binary name or env vars.

5. **Go consumers** (if you use `go install`): the module path is now
   `github.com/mmmarxdr/daimon/cmd/daimon`. Update your `go.mod` accordingly.

### What does NOT change

- Configuration file format — YAML structure is unchanged
- API endpoints — all REST and WebSocket routes are unchanged
- Cookie name (`auth`) — unchanged
- Data format — existing conversations and memory entries are compatible
  after the db rename above

---

*Older pre-0.4.0 entries are not documented here (pre-public-release history).*
