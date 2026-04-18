# Daimon

**The lightweight agent that gives your LLM hands, eyes, and a voice.**

<!-- SCREENSHOT: web dashboard chat view OR the TUI in action.
     Recommended size: 1600x900, .png, commit it to docs/assets/daimon.png -->

<img width="1541" height="925" alt="image" src="https://github.com/user-attachments/assets/ba522b15-055e-4d18-bb8f-d6d853f239d2" />


> **Naming note.** The project is **Daimon**. During the 0.x phase the CLI
> binary is still published as `microagent` from the legacy package name —
> a rename is planned for a future release. All documentation uses the
> `microagent` command until that migration lands.

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
curl -fsSL https://raw.githubusercontent.com/mmmarxdr/micro-claw/main/install.sh | sh
```

For other install paths (release binary, build from source, `go install`),
see **[docs/INSTALL.md](docs/INSTALL.md)**.

### Run it

```bash
microagent web
```

On first run, a browser-based setup wizard launches automatically. It walks
you through provider, API key, and model — validates the key with a real
API call, writes `~/.microagent/config.yaml`, and drops you into the
dashboard.

Prefer the terminal? `microagent --setup` runs the same wizard in a TUI.

Prefer a hand-written config? See **[docs/CONFIG.md](docs/CONFIG.md)**.

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

Early development. Expect breaking changes until 1.0. The CLI rename from
`microagent` to `daimon` is tracked in an open issue and will ship in a
future release.

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
