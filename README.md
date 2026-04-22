# Daimon

**The lightweight agent that gives your LLM hands, eyes, and a voice.**

<!-- SCREENSHOT: web dashboard chat view OR the TUI in action.
     Recommended size: 1600x900, .png, commit it to docs/assets/daimon.png -->

<img width="1541" height="925" alt="image" src="https://github.com/user-attachments/assets/ba522b15-055e-4d18-bb8f-d6d853f239d2" />


> **Note.** As of v0.4.0 the CLI binary is `daimon`. If you were using
> `microagent` from a previous release, see the migration guide in
> [CHANGELOG.md](CHANGELOG.md).

---

## Why Daimon?

LLMs on their own cannot do much. They answer in text. They do not read files,
run commands, remember conversations, or talk to a messaging app. Every useful
AI product you have used is an LLM wrapped in code that gives it those
capabilities.

Daimon is that wrapper, built as a single Go binary with zero runtime
dependencies. It takes an LLM — Claude, GPT, Gemini, a local Ollama model —
and turns it into an agent that can reach into your shell, your files, your
HTTP endpoints, your calendar, your inbox, your chat apps. You bring the
model; Daimon brings the body.

## The name

**Daimon** (Greek: *δαίμων, daímōn*) — in ancient Greek, the guiding spirit
that mediated between the human and the divine. Socrates called his own
the *daimonion* — the inner voice that steered his decisions.

The modern word "agent" descends from the same root, through Latin *agēre*
(to act) and the Greek notion of a being that acts on behalf of another.
A Daimon is an intermediary that does the work — exactly what an AI agent is.

## What you get

- **5 LLM providers** — OpenRouter, Anthropic, OpenAI, Gemini, Ollama.
  Swap with a config change. Fallback provider on rate-limit.
- **4 messaging channels + web dashboard** — CLI, Telegram, Discord,
  WhatsApp, and a browser UI with real-time chat.
- **Built-in tools** — shell, file I/O, HTTP fetch, smart web extraction
  (~90% fewer tokens than raw HTML). All sandboxed by default.
- **MCP-native** — connect any Model Context Protocol server (Gmail,
  GitHub, Notion, Calendar, Slack) without code changes.
- **Skills system** — extend the agent by dropping `.md` files.
  Skills inject knowledge and register shell tools on demand.
- **Scheduled tasks** — "Every Monday at 8am, summarize my unread emails."
  Natural language in, cron out.
- **Secure by default** — bearer token on the web dashboard, whitelisted
  shell commands, sandboxed filesystem, audit log.
- **Single binary** — one file, cross-platform (Linux, macOS, Windows).
  No Docker, no Node, no runtime deps.

## Inspiration

Most "AI agent" frameworks I tried pushed me into a specific stack, a heavy
Python runtime, and an architecture designed for demos. I wanted something
I could drop on a $5 VPS, point at a config file, and forget about — with
the same code running locally as a CLI tool and remotely as a web-accessible
dashboard.

Daimon is that agent. It does not chase AGI. It does one thing: loop an LLM
through tools and channels until useful work gets done, then get out of the
way.

---

## Quick start

### Install

One-liner, detects your OS and architecture:

```bash
curl -fsSL https://raw.githubusercontent.com/mmmarxdr/daimon/main/install.sh | sh
```

For other install paths (release binary, build from source, `go install`),
see **[docs/INSTALL.md](docs/INSTALL.md)**.

### Run it

```bash
daimon web
```

On first run, a browser-based setup wizard launches automatically. It walks
you through provider, API key, and model — validates the key with a real
API call, writes `~/.daimon/config.yaml`, and drops you into the
dashboard.

Prefer the terminal? `daimon --setup` runs the same wizard in a TUI.

Prefer a hand-written config? See **[docs/CONFIG.md](docs/CONFIG.md)**.

### Optional: better PDF extraction

Daimon ingests PDFs with a pure-Go parser by default. It handles most simple
PDFs but struggles with academic papers, LaTeX-generated documents, and PDFs
that use CID-encoded fonts (cards in the Memory tab show **`no text`** when
extraction yields nothing).

Installing **poppler-utils** unlocks the `pdftotext` CLI as a higher-quality
extraction backend. Daimon detects it at startup and uses it automatically;
the pure-Go parser remains the fallback. No config changes needed — restart
daimon after installing.

```bash
# Debian / Ubuntu / WSL
sudo apt install poppler-utils

# macOS (Homebrew)
brew install poppler

# Arch
sudo pacman -S poppler
```

---

## Usage

```
daimon [flags]              # Start the agent (CLI, Telegram, Discord, or WhatsApp channel)
daimon web                  # Start the web dashboard with a full agent loop
daimon web token            # Print the current auth token
daimon setup                # Run the interactive setup wizard
daimon doctor               # Check configuration and connectivity
daimon version              # Print version, commit, and build date
daimon update               # Update daimon in place from the latest GitHub release
daimon mcp [subcommand]     # Manage MCP server connections
daimon skills [subcommand]  # Manage skills
daimon cron [subcommand]    # Manage scheduled tasks
daimon costs [subcommand]   # View token cost history
daimon config [subcommand]  # Inspect or edit config
```

**Flags (main binary only):**

| Flag | Description |
| ---- | ----------- |
| `--config <path>` | Path to config file |
| `--web` | Also start web dashboard alongside the agent |
| `--dashboard` | Open read-only TUI dashboard and exit |
| `--setup` | Run the interactive setup wizard (TUI) and exit |
| `--daemon` | Run as background daemon (cron only, no interactive channel) |
| `--prune-memories` | List memory entries that look like transcripts |
| `--confirm` | Actually delete when used with `--prune-memories` |

`daimon web` is the recommended entry point for most users. It starts both the
web dashboard and the full agent loop in one command. If no config is found, it
launches the browser-based setup wizard automatically.

---

## Knowledge Base

Daimon includes a built-in knowledge base. Documents uploaded via the Knowledge
tab in the web dashboard (or via `POST /api/knowledge`) are extracted, chunked,
embedded, and stored in SQLite. On each user message, the most relevant chunks
are retrieved and injected into the LLM context.

**Supported formats**: PDF, DOCX, Markdown, plain text.

**PDF extraction**: Daimon uses a pure-Go parser by default. For academic PDFs
and LaTeX-generated documents, install `poppler-utils` to enable the higher-quality
`pdftotext` backend — Daimon detects and uses it automatically at startup.

**API**: `GET /api/knowledge` lists documents, `POST /api/knowledge` uploads a
file (multipart/form-data), `DELETE /api/knowledge/{id}` removes a document and
all its chunks. All endpoints require the bearer auth token.

---

## RAG Configuration

The RAG subsystem is configured under the `rag:` block. Full field reference
is in [docs/CONFIG.md](docs/CONFIG.md). Key sub-blocks:

### `rag.embedding` — dedicated embedding provider

Pair any chat provider with a dedicated embedding provider. Required for cosine
reranking and HyDE. Supports `openai` and `gemini`.

```yaml
rag:
  enabled: true
  embedding:
    enabled: true
    provider: openai
    model: text-embedding-3-small    # empty = provider's canonical default
    api_key: ${OPENAI_API_KEY}
    base_url: ""                     # empty = provider's standard endpoint
```

### `rag.retrieval` — precision thresholds and neighbor expansion

Filter low-quality candidates and expand context around retrieved chunks.
Note the asymmetry: BM25 scores are negative (lower = better), so
`max_bm25_score` is a ceiling; cosine similarity is positive (higher = better),
so `min_cosine_score` is a floor.

```yaml
rag:
  retrieval:
    neighbor_radius: 1       # include 1 adjacent chunk on each side; 0 = disabled
    max_bm25_score: 0.0      # 0 = no BM25 threshold
    min_cosine_score: 0.0    # 0 = no cosine threshold
```

### `rag.hyde` — Hypothetical Document Embeddings (opt-in)

When enabled, Daimon generates a short hypothetical answer to each query,
embeds it, and uses it as a second retrieval signal merged with the raw query
via Reciprocal Rank Fusion (RRF). Improves recall for semantic queries that
have no keyword overlap with indexed content. Requires `rag.embedding.enabled: true`.

```yaml
rag:
  hyde:
    enabled: false             # opt-in; false by default
    model: ""                  # fallback: summary_model → main provider
    hypothesis_timeout: 10s
    query_weight: 0.3          # raw query weight; HyDE weight = 1 - query_weight
    max_candidates: 20
```

### `rag.metrics` — retrieval instrumentation

In-memory ring buffer recording per-query retrieval events. Always-on by default.

```yaml
rag:
  metrics:
    enabled: true
    buffer_size: 200
```

### `rag.summary_model` — per-document summarization override

```yaml
rag:
  summary_model: ""   # empty = main provider model
```

---

## Metrics

`GET /api/metrics/rag` returns retrieval instrumentation data: aggregates
(mean, p50, p95 for duration, hit counts, threshold rejections) and recent
individual events from the ring buffer. Auth-token required. Returns `501` when
`rag.metrics.enabled` is `false`.

---

## Documentation

| Topic | Document |
| ----- | -------- |
| Install paths (one-liner, release, source, `go install`) | [docs/INSTALL.md](docs/INSTALL.md) |
| Full config reference | [docs/CONFIG.md](docs/CONFIG.md) |
| LLM providers + fallback | [docs/PROVIDERS.md](docs/PROVIDERS.md) |
| Messaging channels (CLI, Telegram, Discord, WhatsApp) | [docs/CHANNELS.md](docs/CHANNELS.md) |
| Built-in tools + smart `web_fetch` | [docs/TOOLS.md](docs/TOOLS.md) |
| MCP integrations (Gmail, Calendar, GitHub, etc.) | [docs/MCP.md](docs/MCP.md) |
| Skills system | [docs/SKILLS.md](docs/SKILLS.md) |
| Scheduled tasks (cron + natural language) | [docs/CRON.md](docs/CRON.md) |
| Notifications engine | [docs/NOTIFICATIONS.md](docs/NOTIFICATIONS.md) |
| Web dashboard | [docs/WEB_DASHBOARD.md](docs/WEB_DASHBOARD.md) |
| Deploy on a VPS (Caddy + systemd) | [docs/DEPLOY.md](docs/DEPLOY.md) |
| Development (build, test, project layout) | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) |

---

## Tech stack

Go, Bubbletea (TUI), chi (HTTP), SQLite (optional persistence), the official
Model Context Protocol Go SDK. Web frontend shipped as pre-built assets
inside the binary.

## Status

Early development. Expect breaking changes until 1.0. v0.4.0 completed the
rename from `microagent` to `daimon` — see [CHANGELOG.md](CHANGELOG.md)
for migration steps.

## Contributing

Contributions are welcome. Read **[CONTRIBUTING.md](CONTRIBUTING.md)**
before opening a PR.

## License

Daimon is licensed under the [Apache License 2.0](./LICENSE).

Copyright © 2026 Marc Dechand. See [NOTICE](./NOTICE) for attribution requirements.

You are free to use, modify, and distribute Daimon — including for commercial
purposes — provided you preserve the copyright, license, and NOTICE files in
any redistribution. Apache 2.0 includes an explicit patent grant that protects
both users and contributors.
