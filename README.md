# microagent

> A minimal, extensible AI agent in Go. Single binary, zero runtime deps.

`microagent` connects an LLM to channels (CLI, Telegram, Discord, WhatsApp) and a web dashboard through a built-in tool loop. The agent receives a message, builds context from conversation history and memory, calls the LLM, executes any tool calls, and loops until a final response is produced.

**What's included:**

- 5 LLM providers (OpenRouter, Anthropic, Gemini, OpenAI, Ollama)
- 4 messaging channels + web dashboard with real-time chat
- Built-in tools: shell, file I/O, HTTP fetch, MCP protocol
- Skills system (extend the agent with `.md` files)
- Scheduled tasks (cron) with natural language
- TUI dashboard for local monitoring
- Bearer token auth on the web dashboard (secure by default)
- Single binary, cross-platform (Linux, macOS, Windows)

---

## Install

### Option A: Download a release (recommended)

Go to [Releases](https://github.com/mmmarxdr/micro-claw/releases) and download the binary for your platform. Release binaries include the web frontend.

```bash
# Linux (amd64)
tar -xzf microagent_*_linux_amd64.tar.gz
chmod +x microagent
sudo mv microagent /usr/local/bin/
```

### Option B: Build from source

```bash
git clone https://github.com/mmmarxdr/micro-claw.git
cd micro-claw

# TUI-only (no Node.js needed)
make build

# With web frontend (downloads pre-built assets, no Node.js needed)
make build-full

# Binary is at bin/microagent
```

### Option C: Go install

```bash
go install github.com/mmmarxdr/micro-claw/cmd/microagent@latest
```

> Note: `go install` builds without the web frontend. The TUI and API still work. To add the frontend, see [Web Dashboard](#web-dashboard).

---

## Quick Start

**1. Run the setup wizard:**

```bash
microagent
```

On first run (no config found), an interactive TUI wizard launches automatically. It walks you through:

| Step | What |
|------|------|
| Provider | OpenRouter, Anthropic, Gemini, OpenAI, or Ollama |
| Model & API key | Pre-filled default + your API key |
| Channel | CLI, Telegram, or Discord |
| Store path | Where conversations are saved (default: `~/.microagent/data`) |

The wizard writes `~/.microagent/config.yaml` and starts the agent.

**2. Or create a config manually:**

```bash
cp configs/default.yaml.example ~/.microagent/config.yaml
# Edit the file and replace REPLACE_ME values
microagent
```

Minimal config for CLI + OpenRouter:

```yaml
agent:
  name: "Micro"
  personality: "You are a concise, helpful assistant."

provider:
  type: openrouter
  model: openrouter/auto
  api_key: "sk-or-v1-..."   # or: ${OPENROUTER_API_KEY}

channel:
  type: cli

store:
  type: sqlite
  path: "~/.microagent/data"
```

**3. Start chatting.** Type a message and press Enter. The agent responds, using tools as needed.

---

## Web Dashboard

The web dashboard gives you a browser-based UI with real-time chat, metrics, conversation history, and config management.

### Start the dashboard

```bash
# Standalone (web is the only interface)
microagent web

# Alongside CLI/Telegram (both work simultaneously)
microagent --web
```

On startup, the agent generates an auth token and prints it to the console:

```
INFO web dashboard available url=http://127.0.0.1:8080 auth_token=a1b2c3d4...
```

Open the URL in your browser, enter the token, and you're in.

### Auth token options

The dashboard is **always authenticated**. Three ways to set the token:

| Method | Example |
|--------|---------|
| Config file | `web.auth_token: "my-secret-token"` |
| Environment variable | `MICROAGENT_WEB_TOKEN=my-secret-token microagent web` |
| Auto-generated | Leave empty, token is printed on startup |

For production/VPS, set a fixed token in config or env so it persists across restarts.

### Config

```yaml
web:
  enabled: true
  port: 8080
  host: "127.0.0.1"        # 0.0.0.0 to expose to network
  auth_token: ""            # auto-generated if empty
```

### API endpoints

All `/api/*` and `/ws/*` endpoints require `Authorization: Bearer <token>`. WebSocket endpoints accept `?token=<token>` as an alternative.

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/status` | GET | Agent status, uptime, version |
| `/api/config` | GET | Active config (secrets masked) |
| `/api/conversations` | GET | List conversations |
| `/api/conversations/{id}` | GET/DELETE | Get or delete a conversation |
| `/api/memory` | GET/POST | List or create memory entries |
| `/api/memory/{id}` | DELETE | Delete a memory entry |
| `/api/metrics` | GET | Token usage and cost metrics |
| `/api/mcp/servers` | GET | MCP server status |
| `/ws/chat` | WS | Real-time chat with the agent |
| `/ws/metrics` | WS | Live metrics stream |
| `/ws/logs` | WS | Live audit log stream |

---

## Deploy on a VPS

For running microagent on a server with the web dashboard accessible remotely.

### 1. Install

```bash
# Download the latest release
curl -sL https://github.com/mmmarxdr/micro-claw/releases/latest/download/microagent_linux_amd64.tar.gz | tar -xz
sudo mv microagent /usr/local/bin/
```

### 2. Configure

```bash
mkdir -p ~/.microagent
cat > ~/.microagent/config.yaml << 'EOF'
agent:
  name: "Micro"
  personality: "You are a helpful assistant."

provider:
  type: openrouter
  model: google/gemini-2.0-flash-001
  api_key: ${OPENROUTER_API_KEY}
  stream: true

channel:
  type: cli

store:
  type: sqlite
  path: "~/.microagent/data"

web:
  enabled: true
  port: 8080
  host: "127.0.0.1"          # keep localhost — Caddy handles external traffic
  auth_token: ${MICROAGENT_WEB_TOKEN}

audit:
  enabled: true
  path: "~/.microagent/audit"
EOF
```

### 3. Set secrets

```bash
# Add to ~/.bashrc or use a secrets manager
export OPENROUTER_API_KEY="sk-or-v1-..."
export MICROAGENT_WEB_TOKEN="$(openssl rand -hex 32)"
echo "Your web token: $MICROAGENT_WEB_TOKEN"
```

### 4. Reverse proxy with HTTPS (Caddy)

```bash
sudo apt install caddy
```

```
# /etc/caddy/Caddyfile
agent.yourdomain.com {
    reverse_proxy localhost:8080
}
```

```bash
sudo systemctl reload caddy
```

Caddy automatically provisions a Let's Encrypt TLS certificate. Your dashboard is now at `https://agent.yourdomain.com`.

### 5. Run as a systemd service

```ini
# /etc/systemd/system/microagent.service
[Unit]
Description=microagent AI agent
After=network.target

[Service]
Type=simple
User=microagent
Environment=OPENROUTER_API_KEY=sk-or-v1-...
Environment=MICROAGENT_WEB_TOKEN=your-fixed-token-here
ExecStart=/usr/local/bin/microagent web
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /bin/false microagent
sudo systemctl daemon-reload
sudo systemctl enable --now microagent
```

### 6. Verify

```bash
# Check the service
sudo systemctl status microagent

# Test the API
curl -H "Authorization: Bearer $MICROAGENT_WEB_TOKEN" https://agent.yourdomain.com/api/status
```

---

## Providers

| Name | `type` | Env var | Default model |
|------|--------|---------|---------------|
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY` | `openrouter/auto` |
| Anthropic | `anthropic` | `ANTHROPIC_API_KEY` | `claude-sonnet-4-6` |
| Gemini | `gemini` | `GEMINI_API_KEY` | `gemini-2.0-flash` |
| OpenAI | `openai` | `OPENAI_API_KEY` | `gpt-4o` |
| Ollama | `openai` | -- | `llama3` |

A **fallback provider** can be configured under `provider.fallback` with the same fields. It activates on rate-limit or unavailability.

---

## Channels

| Name | `type` | Required fields | Notes |
|------|--------|-----------------|-------|
| CLI | `cli` | None | Reads stdin, writes stdout |
| Telegram | `telegram` | `token`, `allowed_users` | Bot API token from @BotFather |
| Discord | `discord` | `token`, `allowed_users` | Discord bot via WebSocket gateway |
| WhatsApp | `whatsapp` | `token`, `verify_token`, `phone_number_id`, `allowed_phones` | WhatsApp Cloud API webhook |

---

## Built-in Tools

| Tool | Description | Key config |
|------|-------------|------------|
| `shell_exec` | Runs a whitelisted shell command | `tools.shell.allowed_commands` |
| `read_file` | Reads a file within the sandbox | `tools.file.base_path` |
| `write_file` | Writes a file within the sandbox | `tools.file.base_path` |
| `list_files` | Lists a directory within the sandbox | `tools.file.base_path` |
| `http_fetch` | HTTP GET/POST to external URLs | `tools.http.blocked_domains` |

All file tools sandbox paths under `base_path`. Path traversal beyond the sandbox is rejected.

---

## MCP (Model Context Protocol)

Connect external tool servers at runtime without recompiling.

```yaml
tools:
  mcp:
    enabled: true
    connect_timeout: 10s
    servers:
      - name: filesystem
        transport: stdio
        command: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      - name: remote
        transport: http
        url: "http://localhost:8080/sse"
        prefix_tools: true   # tools appear as "remote_<name>"
```

Manage MCP servers:

```bash
microagent mcp list
microagent mcp add --name X --transport stdio --command "npx ..."
microagent mcp remove <name>
microagent mcp test <name>       # connect and list tools
```

---

## Skills

Skills are `.md` files that inject knowledge into the system prompt and register shell tools.

````markdown
---
name: git-helper
description: Git workflow assistant
version: 1.0.0
---

You are an expert at Git workflows. Prefer rebase over merge.

```yaml tool
name: git_log_pretty
description: Show recent commits
command: git log --oneline --graph -20
timeout: 10s
```
````

```bash
microagent skills add https://example.com/react-patterns.md
microagent skills list
microagent skills info <name>
microagent skills remove <name>
```

Tool priority: **built-in > skill > MCP**.

---

## Scheduled Tasks (Cron)

Schedule recurring tasks in natural language. Requires `store.type: sqlite` and `cron.enabled: true`.

```
> every morning at 9am give me a summary of the weather
```

```bash
microagent cron list              # show all jobs
microagent cron info <id>         # job details + results
microagent cron delete <id>       # remove a job
microagent --daemon               # run cron-only (no interactive channel)
```

---

## CLI Reference

```bash
microagent                          # start the agent (setup wizard if no config)
microagent --web                    # start with web dashboard
microagent --dashboard              # TUI dashboard (read-only)
microagent --setup                  # re-run setup wizard
microagent --daemon                 # cron-only background mode

microagent web [--port N] [--host H]  # web-only mode
microagent setup                      # setup wizard
microagent doctor                     # validate config
microagent config                     # show active config

microagent mcp list|add|remove|test|validate|manage
microagent skills add|list|info|remove
microagent cron list|info|delete
```

Config search order: `--config` flag > `~/.microagent/config.yaml` > `./config.yaml`.

All string values support `${ENV_VAR}` interpolation. Paths support `~` expansion.

---

## Configuration Reference

<details>
<summary>Full config reference (click to expand)</summary>

### `agent`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"Micro"` | Agent name |
| `personality` | string | -- | System prompt injected at every turn |
| `max_iterations` | int | `10` | Max tool-use cycles per message |
| `max_tokens_per_turn` | int | `4096` | Max tokens per LLM call |
| `history_length` | int | `20` | Conversation messages kept in context |
| `memory_results` | int | `5` | Max memory entries injected into context |

### `provider`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | -- | `openrouter`, `anthropic`, `gemini`, `openai` |
| `model` | string | Provider default | Model identifier |
| `api_key` | string | -- | API key (supports `${ENV_VAR}`) |
| `timeout` | duration | `60s` | Per-request timeout |
| `max_retries` | int | `3` | Retries on 5xx errors |
| `stream` | bool | `true` | Enable streaming responses |
| `fallback` | object | -- | Fallback provider (same fields) |

### `channel`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | -- | `cli`, `telegram`, `discord`, `whatsapp` |
| `token` | string | -- | Bot API token (Telegram/Discord) |
| `allowed_users` | []int64 | -- | User ID whitelist (Telegram/Discord) |

### `web`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable web dashboard (also via `--web` flag) |
| `port` | int | `8080` | HTTP port |
| `host` | string | `"127.0.0.1"` | Bind address (`0.0.0.0` for network access) |
| `auth_token` | string | auto-generated | Bearer token for API/WS auth; also `MICROAGENT_WEB_TOKEN` env var |

### `tools.shell`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable `shell_exec` tool |
| `allowed_commands` | []string | `[ls, cat, ...]` | Whitelisted commands |
| `allow_all` | bool | `false` | Allow any command |
| `working_dir` | string | `"~"` | Working directory |

### `tools.file`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable file tools |
| `base_path` | string | `"~/workspace"` | Sandbox root |
| `max_file_size` | string | `"1MB"` | Max file size for read/write |

### `tools.http`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable `http_fetch` tool |
| `timeout` | duration | `15s` | Per-request timeout |
| `max_response_size` | string | `"512KB"` | Max response body size |
| `blocked_domains` | []string | `[]` | Blocked domains |

### `tools.mcp`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable MCP client |
| `connect_timeout` | duration | `10s` | Connection timeout |
| `servers[].name` | string | -- | Server logical name |
| `servers[].transport` | string | -- | `stdio` or `http` |
| `servers[].command` | []string | -- | Command for stdio transport |
| `servers[].url` | string | -- | URL for http transport |

### `store`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `"file"` | `file` or `sqlite` |
| `path` | string | `"~/.microagent/data"` | Storage directory |

### `logging`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `"info"` | `debug`, `info`, `warn`, `error` |
| `format` | string | `"text"` | `text` or `json` |

### `limits`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `tool_timeout` | duration | `30s` | Max time per tool execution |
| `total_timeout` | duration | `120s` | Max time per agent turn |

### `audit`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable audit log |
| `path` | string | `"~/.microagent/audit"` | Audit log directory |

### `cron`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable cron scheduler |
| `timezone` | string | `"UTC"` | Timezone for expressions |
| `retention_days` | int | `30` | Delete results older than N days |
| `max_concurrent` | int | `4` | Max concurrent agent turns |

### `skills`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `skills` | []string | `[]` | Paths to skill files |
| `skills_dir` | string | `~/.microagent/skills` | Installed skills directory |

</details>

---

## Development

```bash
make build          # compile binary (TUI-only)
make build-full     # compile with web frontend
make frontend       # download pre-built frontend assets
make copy-frontend  # copy from local micro-claw-frontend checkout
make test           # unit tests
make test-race      # unit tests with race detector
make lint           # golangci-lint
make ci             # vet + lint + test-race
```

### Project structure

```
cmd/microagent/       # entrypoint, subcommands
internal/
  agent/              # agent loop, context builder
  channel/            # CLI, Telegram, Discord, WhatsApp, Web
  provider/           # OpenRouter, Anthropic, Gemini, OpenAI
  tool/               # shell, file, HTTP, MCP tools
  store/              # SQLite persistence
  web/                # HTTP server, REST API, WebSocket, auth
  mcp/                # MCP client (stdio + http)
  cron/               # scheduler, daemon mode
  skill/              # skill loader, parser
  config/             # YAML config, validation
  audit/              # audit log
configs/              # example config + skill files
```

For the full architecture breakdown, see [MICROAGENT.md](./MICROAGENT.md).

---

## License

TBD (MIT)
