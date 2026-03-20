# microagent

> A minimal, extensible AI agent in Go. Single binary, zero runtime deps.

![Go version](https://img.shields.io/badge/go-1.22+-blue)
![License](https://img.shields.io/badge/license-TBD%20(MIT)-lightgrey)

---

## Overview

`microagent` connects an LLM provider to one or more channels (CLI, Telegram) through a built-in tool loop. A message arrives on a channel, the agent builds context from conversation history and memory, calls the provider, and executes any tool calls the model requests — looping until the model produces a final text response. Every component (channel, provider, tool, store) is an interface; adding new ones requires zero changes to the core loop.

For a full architecture breakdown including interfaces, data flows, error handling, and the phased roadmap, see [MICROAGENT.md](./MICROAGENT.md).

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Go | 1.22+ | Required to build |
| gofumpt | latest | Optional — code formatting |
| golangci-lint | latest | Optional — linting |

---

## Quick Start

```bash
# 1. Clone
git clone https://github.com/mmmarxdr/micro-claw.git
cd micro-claw

# 2. Create your personal config from the example (it is git-ignored)
cp configs/default.yaml.example configs/default.yaml
# Edit configs/default.yaml and replace every REPLACE_ME value (see below)

# 3. Run via the developer script — it picks up configs/default.yaml automatically
./dev.sh run
```

### Setting up `configs/default.yaml`

`configs/default.yaml` is **git-ignored** — it is the right place to store real API keys and tokens. Never commit it.

1. Copy the example:
   ```bash
   cp configs/default.yaml.example configs/default.yaml
   ```

2. Open `configs/default.yaml` and replace the `REPLACE_ME` placeholders:

   | Placeholder | Where to get it |
   |---|---|
   | `provider.api_key` | [openrouter.ai/keys](https://openrouter.ai/keys) · [console.anthropic.com](https://console.anthropic.com/) · [aistudio.google.com](https://aistudio.google.com/apikey) |
   | `channel.token` | [@BotFather](https://t.me/BotFather) on Telegram (only needed for Telegram channel) |
   | `channel.allowed_users` | Your numeric Telegram user ID — send a message to [@userinfobot](https://t.me/userinfobot) to find it |

3. Run:
   ```bash
   ./dev.sh run
   ```

> **Alternative — environment variables:** export the key before running and `dev.sh` will use it without reading the file:
> ```bash
> export OPENROUTER_API_KEY="sk-or-v1-..."
> ./dev.sh run
> ```

Minimal config for a CLI agent with OpenRouter:

```yaml
agent:
  name: "Micro"
  personality: "You are a concise, helpful assistant."
  max_iterations: 10

provider:
  type: openrouter
  model: openrouter/auto
  api_key: "sk-or-v1-..."   # or: ${OPENROUTER_API_KEY}

channel:
  type: cli

store:
  type: file
  path: "~/.microagent/data"
```

---

## Configuration

The agent searches for config in order: `--config` flag → `~/.microagent/config.yaml` → `./config.yaml`. String values support `${ENV_VAR}` interpolation. Paths with `~` are expanded to the home directory.

### `agent`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"Micro"` | Agent name, used in logs and prompts |
| `personality` | string | — | System prompt injected at every turn |
| `max_iterations` | int | `10` | Max tool-use cycles per user message |
| `max_tokens_per_turn` | int | `4096` | Max tokens per LLM call |
| `history_length` | int | `20` | Conversation messages kept in context |
| `memory_results` | int | `5` | Max memory entries injected into context |

### `provider`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | Provider implementation (`openrouter`, `anthropic`, `gemini`) |
| `model` | string | Provider default | Model identifier |
| `api_key` | string | — | API key; use `${ENV_VAR}` |
| `timeout` | duration | `60s` | Per-request HTTP timeout |
| `max_retries` | int | `3` | Retries on 5xx errors |
| `fallback` | object | — | Optional fallback provider (same fields); activated on rate-limit or unavailability |

### `channel`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | Channel implementation (`cli`, `telegram`) |
| `token` | string | — | Telegram Bot API token (Telegram only) |
| `allowed_users` | []int64 | — | Telegram user ID whitelist (Telegram only) |

### `tools.shell`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable `shell_exec` tool |
| `allowed_commands` | []string | `[ls, cat, ...]` | Whitelisted base commands |
| `allow_all` | bool | `false` | Allow any command — use with caution |
| `working_dir` | string | `"~"` | Working directory for command execution |

### `tools.file`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable file tools (`read_file`, `write_file`, `list_files`) |
| `base_path` | string | `"~/workspace"` | Sandbox root; all paths are relative to this |
| `max_file_size` | string | `"1MB"` | Reject reads/writes above this size |

### `tools.http`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable `http_fetch` tool |
| `timeout` | duration | `15s` | Per-request timeout |
| `max_response_size` | string | `"512KB"` | Truncate responses above this size |
| `blocked_domains` | []string | `[]` | Domains the tool will never fetch |

### `tools.mcp`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable MCP client |
| `connect_timeout` | duration | `10s` | Timeout for connecting to each MCP server |
| `servers[].name` | string | — | Logical name for the server |
| `servers[].transport` | string | — | `stdio` or `http` |
| `servers[].command` | []string | — | Command + args for `stdio` transport |
| `servers[].url` | string | — | URL for `http` (SSE) transport |
| `servers[].prefix_tools` | bool | `false` | Prefix tool names with the server name |

### `store`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `"file"` | Storage backend (`file`; `sqlite` planned) |
| `path` | string | `"~/.microagent/data"` | Root directory for file store |

### `logging`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `"info"` | Log level: `debug`, `info`, `warn`, `error` |
| `format` | string | `"text"` | Log format: `text` or `json` |

### `limits`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tool_timeout` | duration | `30s` | Max time for a single tool execution |
| `total_timeout` | duration | `120s` | Max time for the entire agent turn |

### `audit`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Write an audit log of all tool executions |
| `path` | string | `"~/.microagent/audit"` | Directory for audit log files |

### `skills`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `skills` | []string | `[]` | Paths to `.md` skill files loaded at startup |
| `skills_dir` | string | `~/.microagent/skills` | Directory where `microagent skills add` stores installed skills |
| `skills_registry_url` | string | `""` | Base URL for short-name skill resolution (e.g. `microagent skills add git-helper`) |

Paths in `skills` support `~` expansion. Example:

```yaml
skills:
  - ~/.microagent/skills/git-helper.md
  - ~/my-skills/react-patterns.md

skills_dir: ~/.microagent/skills
# skills_registry_url: https://skills.example.com
```

### `cron`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the cron scheduler |
| `timezone` | string | `"UTC"` | Timezone for cron expressions (e.g. `"America/New_York"`) |
| `retention_days` | int | `30` | Delete job results older than this many days |
| `max_results_per_job` | int | `50` | Keep at most this many results per job |
| `max_concurrent` | int | `4` | Max concurrent agent turns (cron + interactive share this pool) |

Cron requires `store.type: sqlite`. Example:

```yaml
cron:
  enabled: true
  timezone: "America/New_York"
  retention_days: 30
  max_results_per_job: 50
```

---

## Providers

| Name | `type` | Env var | Default model | Notes |
|------|--------|---------|---------------|-------|
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY` | `openrouter/auto` | Routes to best available model |
| Anthropic | `anthropic` | `ANTHROPIC_API_KEY` | `claude-3-5-sonnet-20241022` | Direct Anthropic API |
| Gemini | `gemini` | `GEMINI_API_KEY` | `gemini-2.0-flash` | Google Gemini API |

A **fallback provider** can be configured under `provider.fallback`. It uses the same fields and is activated when the primary returns a rate-limit or unavailability error.

---

## Channels

| Name | `type` | Required fields | Notes |
|------|--------|-----------------|-------|
| CLI | `cli` | None | Reads stdin, writes stdout |
| Telegram | `telegram` | `token`, `allowed_users` | Requires a Bot API token from @BotFather |

---

## Built-in Tools

| Tool | What it does | Key config |
|------|-------------|------------|
| `shell_exec` | Runs a whitelisted shell command, returns combined stdout/stderr (10KB max) | `tools.shell.allowed_commands`, `tools.shell.working_dir` |
| `read_file` | Reads a file relative to `base_path` | `tools.file.base_path`, `tools.file.max_file_size` |
| `write_file` | Writes a file relative to `base_path`, creates directories as needed | `tools.file.base_path` |
| `list_files` | Lists a directory relative to `base_path` | `tools.file.base_path` |
| `http_fetch` | HTTP GET or POST to an external URL, returns response body | `tools.http.timeout`, `tools.http.blocked_domains` |

All file tools sandbox paths under `base_path`. Path traversal beyond the sandbox is rejected.

---

## MCP (Model Context Protocol)

MCP lets the agent connect to external tool servers at runtime, without recompiling. Tools from MCP servers appear alongside built-in tools; the agent loop does not distinguish between them.

Supported transports:
- **`stdio`** — spawns a subprocess and communicates over stdin/stdout
- **`http`** — connects to an SSE endpoint

```yaml
tools:
  mcp:
    enabled: true
    connect_timeout: 10s
    servers:
      - name: filesystem
        transport: stdio
        command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        prefix_tools: false
      - name: myserver
        transport: http
        url: "http://localhost:8080/sse"
        prefix_tools: true   # tools appear as "myserver_<name>"
```

---

## Skills

Skills are `.md` files that extend the agent with two capabilities:

1. **Prose injection** — the skill's body text is appended to the system prompt at startup, giving the agent domain knowledge or behavioral instructions.
2. **Shell tools** — fenced ` ```yaml tool ` blocks register fixed shell commands as agent tools. The LLM can call them by name but cannot modify their command — unlike `shell_exec`, skill tools bypass the whitelist because the command is fixed at load time by the user.

### Skill file format

````markdown
---
name: git-helper
description: Git workflow assistant
version: 1.0.0
author: you
---

You are an expert at Git workflows. Prefer rebase over merge for feature branches.

```yaml tool
name: git_log_pretty
description: Show recent commits in a readable format
command: git log --oneline --graph --decorate -20
timeout: 10s
```
````

### Installing skills

```bash
# From a URL
microagent skills add https://example.com/react-patterns.md

# From a local file
microagent skills add ./my-skill.md

# Short name (requires skills_registry_url in config)
microagent skills add git-helper
```

> **Security note:** skills installed from URLs write files that execute shell commands with your user privileges. Only install skills from sources you trust. A warning is always printed before any URL fetch.

### Managing skills

```bash
microagent skills list           # list registered skills
microagent skills list --store   # also show unregistered files in skills_dir
microagent skills info <name>    # show frontmatter, prose, and tools
microagent skills remove <name>  # unregister and delete from store
microagent skills remove <name> --keep-file  # unregister only
```

### Tool priority

When two tools share a name, the resolution order is: **built-in > skill > MCP**. A skill tool always wins over an MCP server tool with the same name.

---

## Scheduled Tasks (Cron)

The cron system lets you schedule recurring tasks in natural language. Enable it in config (`cron.enabled: true`, requires SQLite store), load `configs/skills/cron.md` as a skill, and the agent will understand scheduling intent.

### Scheduling a task

Tell the agent what you want and when:

```
every morning at 9am give me a summary of my unread emails
```

Or use the explicit `/cron` prefix. The agent calls `schedule_task` internally, converts the schedule to a cron expression via an LLM sub-call, and confirms back:

```
✓ Scheduled: 'give me a summary of my unread emails'
Schedule: every day at 9:00 AM (cron: 0 9 * * *)
Next run: Sat, 21 Mar 2026 09:00:00 UTC
Job ID: a1b2c3d4e5f6
```

If a required tool or MCP is missing (e.g. you asked about email but have no email MCP configured), the agent warns you and suggests the setup command.

### Managing scheduled tasks

Ask the agent, or use the CLI directly (no running agent needed):

```bash
microagent cron list              # show all scheduled jobs
microagent cron info <id>         # show job details + last 10 results
microagent cron delete <id>       # remove a job
```

The agent also understands natural requests: "show my scheduled tasks", "cancel the email cron".

### Daemon mode

Run the agent in background mode (no interactive channel — cron only):

```bash
microagent --daemon
# or as a systemd service
```

In daemon mode, job results are stored in `cron_results` and sent back to the originating channel (CLI or Telegram) that created the job.

### cron.md skill

To enable cron UX, add `configs/skills/cron.md` to your skills list:

```yaml
skills:
  - configs/skills/cron.md
```

Without this skill, the agent has no instructions to recognize scheduling intent — the tools exist but the agent won't know when to use them.

---

## Running

```bash
./microagent [flags]
```

| Flag | Description |
|------|-------------|
| `-config <path>` | Path to config YAML (overrides auto-search) |
| `-version` | Print version and exit |
| `-dashboard` | Open read-only TUI dashboard and exit |
| `-setup` | Run the interactive setup wizard and exit |

### Subcommands

```bash
microagent mcp list                   # list configured MCP servers
microagent mcp add --name X --transport stdio --command "npx ..."
microagent mcp remove <name>
microagent mcp test <name>            # connect and list tools
microagent mcp validate               # validate MCP config section
microagent mcp manage                 # interactive TUI management screen

microagent skills add <url|path|name> # install a skill
microagent skills list [--store]      # list installed skills
microagent skills remove <name>       # uninstall a skill
microagent skills info <name>         # show skill details

microagent cron list              # list scheduled cron jobs
microagent cron info <id>         # show job details and last results
microagent cron delete <id>       # delete a scheduled job
microagent --daemon               # run in background mode (cron only)
```

All subcommands accept `--config <path>` to override the config file location.

Auto-search order: `~/.microagent/config.yaml` → `./config.yaml`.

### First-run setup wizard

When no config file is found and `microagent` is started in an interactive terminal, it automatically launches a TUI setup wizard instead of exiting with an error.

> **Note**: micro-claw searches for your config in two locations (in order):
> 1. `~/.microagent/config.yaml` (user home)
> 2. `./config.yaml` (current directory)
>
> If you delete one but the other exists, the wizard will not re-launch. Use `./microagent --setup` to force the wizard regardless.

The wizard walks through five steps:

| Step | What it collects |
|------|-----------------|
| 1 — Provider | Provider type: `anthropic`, `gemini`, `openrouter`, `openai`, or `ollama` |
| 2 — Model & API Key | Model identifier (pre-filled with a sensible default) and the API key |
| 3 — Channel | Channel type: `cli`, `telegram`, or `discord` |
| 3b — Channel extras | Bot token and allowed user IDs (Telegram/Discord only) |
| 4 — Store path | Data store directory (default: `~/.microagent/data`) |
| 5 — Confirm | YAML preview; press Enter to write the config |

The config is written to `~/.microagent/config.yaml`. The agent starts normally once the wizard completes.

**Keyboard controls in the wizard:**

| Key | Action |
|-----|--------|
| `Enter` | Advance to next step |
| `Esc` | Go back one step (or abort at step 1) |
| `Tab` / `Shift+Tab` | Switch between fields on steps with multiple inputs |
| `Ctrl+C` | Quit without saving |

To re-run the wizard at any time (e.g. to change provider or fix a broken config):

```bash
./microagent --setup
```

**Non-interactive mode:** if stdin is not a terminal (e.g. piped input, CI), the wizard does not launch. The process prints a message and exits:

```
No config file found. Create one at ~/.microagent/config.yaml before running in non-interactive mode.
```

### Dashboard

The `--dashboard` flag opens a read-only TUI dashboard that displays stats and config from the current run. No agent loop is started; the process exits when you quit the dashboard.

```bash
./microagent --dashboard
# or with a non-default config:
./microagent -config /path/to/config.yaml --dashboard
```

**Tabs:**

| Tab | Content |
|-----|---------|
| Overview | Audit DB path, total events, LLM call count, average token usage, tool call count and success rate, timestamp of last event |
| Audit Events | Scrollable table of recent audit records (ID, type, model, tokens, duration, tool status) |
| Store | Conversation count, memory entries, and secrets count from the data store |
| Config | Active provider, model, channel type, store path, and audit path (API key always redacted) |
| MCP | Configured MCP servers with name, transport, command/URL, and status; press `e` to open the interactive MCP management screen |

**Keyboard controls in the dashboard:**

| Key | Action |
|-----|--------|
| `Tab` / `→` | Switch to next tab |
| `←` | Switch to previous tab |
| `q` / `Ctrl+C` | Quit |

The dashboard requires an interactive terminal (stdout must be a TTY). Running it without one prints an error and exits:

```
Dashboard requires an interactive terminal.
```

---

## Testing

**Unit tests** — no external dependencies required:

```bash
go test ./...
```

**Integration tests** — test the full MCP client stack. The test harness compiles a helper MCP server binary at startup:

```bash
go test -tags=integration ./internal/mcp/... -v -timeout 60s
```

Integration tests live in `internal/mcp/integration_test.go` behind the `//go:build integration` tag. The helper server source is in `internal/mcp/testdata/server/`.

Or use the contributor script which runs both:

```bash
./test.sh
```

---

## Development

### Makefile targets

| Target | What it does |
|--------|-------------|
| `make build` | Compile binary with `-ldflags="-s -w"` |
| `make test` | Run unit tests |
| `make integration-test` | Run integration tests |
| `make lint` | Run `golangci-lint` |
| `make fmt` | Format with `gofumpt` |
| `make dev-run` | Build and run with `./config.yaml` |

### Code style

- Format with `gofumpt` (stricter than `gofmt`): `gofumpt -w .`
- Lint with `golangci-lint`: `golangci-lint run`
- Errors are values — wrap with `%w`, never swallow, never panic on recoverable errors
- Every exported symbol needs a doc comment
- Table-driven tests, no external test frameworks

### Project structure

```
microagent/
├── cmd/microagent/      # Entrypoint: config → wire → run; mcp + skills subcommands
├── internal/
│   ├── agent/           # Agent loop (loop.go), context builder (context.go)
│   ├── channel/         # Channel interface + CLI and Telegram implementations
│   ├── provider/        # Provider interface + OpenRouter/Anthropic/Gemini clients
│   ├── tool/            # Tool interface, registry, shell/file/http tools
│   ├── store/           # Store interface + SQLite persistence
│   ├── mcp/             # MCP client (stdio + http), MCPService (config management)
│   ├── cron/            # Cron scheduler (robfig/cron/v3), CronChannel, daemon mode
│   ├── skill/           # Skill loader, parser, shell tool, SkillService (install)
│   └── config/          # YAML parsing, env var resolution, validation
└── configs/             # Example config and skill files
```

---

## Roadmap

- **Skill registry** — `microagent skills add git-helper` short-name installs from a hosted registry
- **Discord channel** — Discord bot integration alongside CLI and Telegram
- **OpenAI / Ollama providers** — OpenAI-compatible API support and local model inference via Ollama
- **CI/CD pipeline** — Dockerfile, GitLab CI, and Fly.io pre-production environment
