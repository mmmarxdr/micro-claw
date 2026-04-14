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
| `http_fetch` | HTTP GET/POST raw responses | `tools.http.blocked_domains` |
| `web_fetch` | Fetch URL and extract as clean Markdown (~90% fewer tokens) | `tools.web_fetch.jina_enabled` |

All file tools sandbox paths under `base_path`. Path traversal beyond the sandbox is rejected.

### `web_fetch` — Smart Web Content Extraction

`web_fetch` converts web pages to clean Markdown optimized for LLM consumption (~90% fewer tokens than raw HTML). It uses a **3-tier extraction strategy** that automatically escalates when needed:

| Tier | Method | When used | Tokens |
|------|--------|-----------|--------|
| **1** | Local extraction (go-readability + html-to-markdown) | Default — works for most content-rich pages | ~2K for a news article |
| **2** | [Jina Reader API](https://jina.ai/reader/) fallback | When Tier 1 extracts < 200 chars (JS-heavy pages) | ~2K |
| **3** | Raw HTTP response | When `extract_content: false` is passed | Full page size |

The tier used is returned in the tool's metadata (`tier: "1"`, `"2"`, or `"raw"`).

**Configuration:**

```yaml
tools:
  web_fetch:
    enabled: true            # default: true
    timeout: 20s             # default: 20s
    max_response_size: 1MB   # default: 1MB
    blocked_domains: []      # domain blocklist
    jina_enabled: true       # enables Tier 2 fallback (default: false)
    jina_api_key: ""         # optional — or set MICROAGENT_JINA_API_KEY env var
```

**Examples to try with the agent:**

- **Tier 1** (static content): *"Fetch and summarize https://en.wikipedia.org/wiki/Buenos_Aires"*
- **Tier 2** (JS-heavy, needs Jina): *"Fetch https://www.google.com/search?q=microagent+ai"*
- **Tier 3** (raw HTML): *"Fetch https://httpbin.org/html with extract_content false"*

> **Note:** Some sites (X/Twitter, LinkedIn) actively block automated access. Neither Tier 1 nor Tier 2 can bypass authentication walls.

---

## Integrations (MCP)

MCP (Model Context Protocol) lets the agent connect to external services at runtime — no code changes required. MCP servers run as subprocesses (stdio) or HTTP endpoints.

**Requirements**: Most MCP servers need **Node.js >= 18** (for `npx`). Some use Python. The agent itself does not require Node.

### How MCP works

1. You add an MCP server to your config (`tools.mcp.servers[]`)
2. On startup, micro-claw connects to each server and discovers its tools
3. Tools are registered alongside built-ins — the LLM can call them like any other tool
4. Use `prefix_tools: true` to namespace tool names (recommended with multiple servers)

There are two **auth patterns** depending on the server:

| Pattern | How it works | Example |
|---------|-------------|---------|
| **Env-based** | Credentials in env vars, server auto-connects | Gmail (IMAP), GitHub, Brave Search |
| **OAuth** | Server exposes `authenticate` tool, user authorizes in browser first | Google Calendar, Google Workspace |

### Setup: Gmail (env-based auth)

1. Enable [2-Step Verification](https://myaccount.google.com/security) on your Google account
2. Generate an [App Password](https://myaccount.google.com/apppasswords) (name it "microagent")
3. Add to config:

```yaml
tools:
  mcp:
    enabled: true
    connect_timeout: 30s
    servers:
      - name: gmail
        transport: stdio
        command: ["npx", "-y", "mcp-mail-server"]
        prefix_tools: true
        env:
          IMAP_HOST: "imap.gmail.com"
          IMAP_PORT: "993"
          IMAP_SECURE: "true"
          SMTP_HOST: "smtp.gmail.com"
          SMTP_PORT: "465"
          SMTP_SECURE: "true"
          EMAIL_USER: "you@gmail.com"
          EMAIL_PASS: "your-app-password-no-spaces"
```

4. Verify: `microagent mcp test gmail`
5. Ask the agent: *"Show me my unread emails"*

### Setup: Google Calendar (OAuth auth)

OAuth servers require a one-time browser authorization before they work.

**Step 1 — Google Cloud credentials** (one-time):

1. Go to [Google Cloud Console](https://console.cloud.google.com) and create a project
2. Enable the **Google Calendar API** (APIs & Services > Library)
3. Go to **APIs & Services > Credentials > + CREATE CREDENTIALS > OAuth client ID**
   - If prompted, configure the OAuth consent screen (External, add your email as test user)
   - Application type: **Desktop app**
4. Download the JSON file and save it as `~/.microagent/google-credentials.json`

**Step 2 — Authorize** (one-time, run outside micro-claw):

```bash
GOOGLE_OAUTH_CREDENTIALS=/home/you/.microagent/google-credentials.json \
  npx -y @cocal/google-calendar-mcp auth
```

This opens your browser. Authorize and close. The token is cached locally.

**Step 3 — Add to config:**

```yaml
tools:
  mcp:
    servers:
      # ... other servers ...
      - name: google-calendar
        transport: stdio
        command: ["npx", "-y", "@cocal/google-calendar-mcp"]
        prefix_tools: true
        env:
          GOOGLE_OAUTH_CREDENTIALS: "/home/you/.microagent/google-credentials.json"
```

4. Restart and ask: *"What events do I have this week?"*

### Popular integrations

| Server | Install | Auth | What it does |
|--------|---------|------|-------------|
| **Gmail** (IMAP) | `npx -y mcp-mail-server` | Env (app password) | Read, send, search, reply to emails |
| **Google Calendar** | `npx -y @cocal/google-calendar-mcp` | OAuth (browser) | Events, scheduling, free/busy |
| **Google Workspace** | `pip install workspace-mcp` | OAuth (browser) | Gmail + Calendar + Drive + Docs |
| **GitHub** | `npx -y @modelcontextprotocol/server-github` | Env (`GITHUB_TOKEN`) | Issues, PRs, repos, CI |
| **Filesystem** | `npx -y @modelcontextprotocol/server-filesystem /path` | None | Sandboxed file access |
| **Brave Search** | `npx -y @modelcontextprotocol/server-brave-search` | Env (`BRAVE_API_KEY`) | Web search |
| **Notion** | `npx -y @notionhq/notion-mcp-server` | Env (`NOTION_TOKEN`) | Pages, databases, blocks |
| **Slack** | `npx -y @modelcontextprotocol/server-slack` | Env (`SLACK_BOT_TOKEN`) | Channels, messages, threads |

### Auto-installed skills

When you add an MCP server from the catalog (via CLI or web dashboard), micro-claw automatically installs a **skill file** that teaches the LLM how to use that integration efficiently. Skills include:

- Token-saving strategies (fetch counts before content, use small limits)
- Auth flow instructions (for OAuth servers)
- Common task mappings ("show my unread emails" -> which tool to call)

Skills are loaded on-demand via the `load_skill` tool — they don't waste context tokens on every message. See the [Skills](#skills) section for details.

### Managing MCP servers

```bash
microagent mcp list                    # show configured servers
microagent mcp test <name>             # live connection test
microagent mcp add --name X --transport stdio --command "npx ..."
microagent mcp remove <name>
microagent mcp validate                # check config + env vars
```

Or manage from the web dashboard: **Integrations** page (add from catalog, test, remove).

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

Schedule recurring tasks in natural language. The agent converts natural language to cron expressions, executes the prompt autonomously at each tick, and delivers results to the channel where it was created.

**Requirements**: `store.type: sqlite` and `cron.enabled: true`.

```yaml
cron:
  enabled: true
  timezone: America/Argentina/Buenos_Aires   # optional, defaults to UTC
  notify_on_completion: true                  # prefix results with task info
```

**Creating tasks** — just ask the agent:

- *"Every morning at 9am give me a summary of my calendar events"*
- *"Every minute tell me the current time"*
- *"Every Monday at 8am send me a weekly report of my emails"*

The agent uses the `schedule_task` tool automatically. Prompts must be self-contained — the agent runs them without conversation context, so include all relevant details in the prompt.

**Managing tasks:**

| Method | How |
|--------|-----|
| Chat | *"Show my scheduled tasks"* → agent calls `list_crons` |
| Chat | *"Cancel task X"* → agent calls `delete_cron` |
| CLI | `microagent cron list` / `microagent cron delete <id>` |
| Daemon | `microagent --daemon` — runs cron-only, no interactive channel |

**Per-job notification**: Each cron job can override where notifications go via `notify_channel` and `notify_on_completion` fields (set programmatically or via the notification rules below).

---

## Notifications

Push notifications when events happen — cron completions, agent turn status, failures. Notifications are delivered to any configured channel (Telegram, Discord, WhatsApp, Web dashboard).

### How it works

```
Event sources              Notification engine           Delivery channels
┌──────────────┐     ┌───────────────────────────┐     ┌──────────────┐
│ Cron fired   │     │                           │     │ Telegram     │
│ Cron done    │────>│  Event Bus → Rules Engine │────>│ Discord      │
│ Cron failed  │     │  (match + cooldown +      │     │ WhatsApp     │
│ Turn started │     │   template + fallback)    │     │ Web dashboard│
│ Turn done    │     │                           │     │ Email (MCP)  │
└──────────────┘     └───────────────────────────┘     └──────────────┘
```

1. Internal events are emitted to an **event bus** (non-blocking, buffered)
2. A **rules engine** evaluates each event against your configured rules
3. Matching rules render a **Go template** and send via the target channel
4. If delivery fails, an optional **fallback channel** is tried
5. All notifications are **audited** (visible in the Logs dashboard)

### Event types

| Event | When it fires | Useful for |
|-------|---------------|------------|
| `cron.job.fired` | A cron job starts executing | Tracking execution |
| `cron.job.completed` | A cron job finished successfully | Receiving results on another channel |
| `cron.job.failed` | A cron job errored | Failure alerts |
| `agent.turn.started` | The agent begins processing a message | Activity monitoring |
| `agent.turn.completed` | The agent finished responding | Task completion alerts |

### Configuration

```yaml
notifications:
  enabled: true
  max_per_minute: 30          # circuit breaker — prevents notification floods
  bus_buffer_size: 256        # internal event queue size
  handler_timeout_sec: 5      # max seconds to wait for channel delivery
  rules:
    - name: cron-results          # unique name for this rule
      event_type: cron.job.completed
      target_channel: "telegram:7535164458"   # where to send
      template: "✅ {{.JobPrompt}}: {{.Text}}"
      cooldown_sec: 10            # don't fire again for 10s (prevents spam)

    - name: cron-failures
      event_type: cron.job.failed
      target_channel: "telegram:7535164458"
      fallback_channel: "web:broadcast"       # try web if Telegram fails
      template: "⚠ Task '{{.JobPrompt}}' failed: {{.Error}}"

    - name: job-specific
      event_type: cron.job.completed
      job_id: "a22ea2c4-..."      # only this specific job
      target_channel: "telegram:7535164458"
      template: "📊 Report ready: {{.Text}}"

    - name: turn-done
      event_type: agent.turn.completed
      target_channel: "web:broadcast"
      template: "Agent done — {{.Meta.input_tokens}} in / {{.Meta.output_tokens}} out tokens"
      cooldown_sec: 60            # at most once per minute
```

### Template variables

Templates use Go's `text/template` syntax. Available fields in every event:

| Field | Type | Description |
|-------|------|-------------|
| `{{.Type}}` | string | Event type (e.g. `cron.job.completed`) |
| `{{.JobID}}` | string | Cron job ID (empty for agent events) |
| `{{.JobPrompt}}` | string | The cron job's prompt text |
| `{{.ChannelID}}` | string | Originating channel |
| `{{.Text}}` | string | Result text (agent response or cron output) |
| `{{.Error}}` | string | Error message (only on failures) |
| `{{.Timestamp}}` | time.Time | When the event occurred |
| `{{.Meta.input_tokens}}` | string | Input tokens used (turn events only) |
| `{{.Meta.output_tokens}}` | string | Output tokens used (turn events only) |

### Channel ID format

Target channels use the format `<channel_type>:<identifier>`:

| Channel | Format | Example |
|---------|--------|---------|
| Telegram | `telegram:<chat_id>` | `telegram:7535164458` |
| Discord | `discord:<channel_id>` | `discord:123456789` |
| WhatsApp | `whatsapp:<phone>` | `whatsapp:+5491155551234` |
| Web | `web:broadcast` | Sends to all connected web clients |

### Safety features

| Feature | Description |
|---------|-------------|
| **Loop prevention** | Notification events (`notification.sent/failed`) never trigger other rules |
| **Circuit breaker** | `max_per_minute` caps total notifications globally (default: 30) |
| **Per-rule cooldown** | `cooldown_sec` prevents the same rule from firing too frequently |
| **Fallback channel** | If primary delivery fails, tries `fallback_channel` before giving up |
| **Audit trail** | Every notification (sent or failed) is recorded as an audit event |
| **Non-blocking** | Event bus never blocks the agent — events are dropped if the buffer is full |
| **Startup validation** | Rules are validated on startup (template syntax, unique names, known event types) |

### Examples

**Daily calendar summary to Telegram:**
```
1. Ask the agent: "Every day at 9am tell me my calendar events for today"
2. Configure notification rule:
   - event_type: cron.job.completed
   - target_channel: telegram:<your_chat_id>
   - template: "📅 {{.Text}}"
3. Result: every day at 9am, you get a Telegram push with your events
```

**Alert on cron failures:**
```yaml
- name: alert-failures
  event_type: cron.job.failed
  target_channel: "telegram:7535164458"
  template: "🚨 Scheduled task failed!\nTask: {{.JobPrompt}}\nError: {{.Error}}"
```

**Weekly email summary (via Gmail MCP):**
```
Ask the agent: "Every Monday at 8am, summarize my unread emails from
the past week and send the summary to marxdr7@gmail.com using Gmail"
```
The cron job prompt includes the "send email" instruction, so the agent uses the Gmail MCP tool during execution.

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
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/mmmarxdr/micro-claw)

---

## License

TBD (MIT)
