# MicroAgent Dashboard — Design Document

## 1. Overview / Purpose

The MicroAgent Dashboard is a standalone React web application that gives users a visual interface for their running local agent. It replaces the need to read log files, edit YAML configs, or use a separate chat client (Telegram, Discord, CLI) just to interact with the agent.

**Core goals:**

- Observe the agent in real time: costs, token usage, conversation activity
- Interact with the agent through a built-in chat interface
- Configure the agent without touching config files or the terminal
- Inspect conversation history and memory entries

The dashboard is **not** part of the Go binary's core job. It is an optional layer. The agent runs fine without it. When enabled, it starts an HTTP server alongside the agent loop.

---

## 2. Architecture Overview

### Topology

```
┌──────────────────────┐        HTTP / WebSocket        ┌────────────────────────┐
│   React Dashboard    │ ◄────────────────────────────► │   Go HTTP Server       │
│   (browser, :3000)   │                                 │   (agent, :8080)       │
└──────────────────────┘                                 └──────────┬─────────────┘
                                                                    │
                                                         ┌──────────▼─────────────┐
                                                         │   Agent internals      │
                                                         │  Store / Loop / Config │
                                                         └────────────────────────┘
```

### Communication model

| Protocol | Used for |
|----------|----------|
| REST (HTTP/JSON) | All read/write operations: config, conversations, memory, metrics snapshot |
| WebSocket | Real-time: chat messages, streaming agent responses, live metric updates |

### Go HTTP server

A new `internal/api` package wraps the existing agent internals and exposes them over HTTP. It is wired in `main.go` alongside the existing channel/agent startup and runs in its own goroutine. It holds references to the same `Store`, `Config`, and `Agent` instances — no duplication.

```
internal/
  api/
    server.go        # http.Server setup, route registration, CORS
    handlers.go      # REST handler functions
    ws.go            # WebSocket hub + chat relay
    middleware.go    # logging, auth (optional bearer token)
    metrics.go       # in-process metrics accumulator (costs, tokens)
```

The server is **opt-in** via config:

```yaml
dashboard:
  enabled: true
  port: 8080
  host: "127.0.0.1"    # bind to localhost only by default
  auth_token: ""        # optional static bearer token for basic protection
```

### Frontend

A separate `dashboard/` directory at the repo root contains the React project. It is **not embedded** in the Go binary in Phase 1. The developer runs it separately during development. Embedding it as a Go `embed.FS` is a Phase 3 concern.

```
dashboard/
  src/
    components/
    pages/
    hooks/
    api/           # typed API client (fetch + WebSocket)
  package.json
  vite.config.ts
```

---

## 3. Dashboard Sections

### 3.1 Overview (Home)

The landing page. Quick-glance health and cost status.

**Widgets:**

| Widget | Description |
|--------|-------------|
| Status badge | Agent running / stopped / error. Reflects the Go process state via polling or WebSocket ping. |
| Active model | Provider name + model name currently configured (e.g., `anthropic / claude-sonnet-4-5`). |
| Total cost today | USD cost accumulated since midnight, calculated from token counts × provider pricing table stored client-side. |
| Total cost this month | Same, month-to-date. |
| Total tokens today | Input + output tokens, separated. |
| Conversations today | Count of conversations that received at least one message today. |
| Last activity | Timestamp of last processed message. |
| Memory entries | Total count of stored memory entries. |

All data is a snapshot from `GET /api/metrics`. Refreshes every 30 seconds or on WebSocket event.

---

### 3.2 Metrics & Charts

Historical view of usage. Useful for tracking API costs before they become a surprise.

**Charts:**

| Chart | Type | X-axis | Y-axis |
|-------|------|--------|--------|
| Token usage over time | Line chart | Day (last 30 days) | Input tokens / Output tokens (two lines) |
| Cost over time | Bar chart | Day (last 30 days) | USD cost per day |
| Model breakdown | Donut/pie | — | % of total tokens per model/provider |
| Conversations per day | Bar chart | Day (last 14 days) | Message count |

**Data source:** The Go server accumulates per-day usage stats in a lightweight in-process structure (flushed to a `metrics.json` file alongside the store, so it survives restarts). No external database.

**Quota widget:** If the user sets a monthly cost budget in config/settings, this section shows a progress bar: `$X.XX / $Y.YY (Z%)` with a warning color at 80%.

---

### 3.3 Conversations

Browse and inspect conversation history stored by the agent.

**List view:**

- Paginated list of conversations, newest first
- Each row: channel ID badge (cli / telegram / discord), first user message (truncated), last activity timestamp, message count
- Search bar: filters by channel or searches message content
- Click a row → opens conversation detail

**Detail view:**

- Full message thread rendered in a chat-bubble layout
- Each message shows: role (user / assistant / tool), content, timestamp
- Tool calls rendered as collapsible blocks: `▶ shell_exec → ls -la /tmp` → expand to see result
- Read-only. Cannot edit history.

**API calls used:**

```
GET /api/conversations?limit=20&offset=0&channel=cli
GET /api/conversations/:id
```

---

### 3.4 Memory

View and search the agent's long-term memory (key-value entries extracted from conversations).

**Layout:**

- Search box at the top (calls `GET /api/memory?q=...`)
- Results list: each entry shows content, tags (as chips), source conversation ID (links to conversation detail), and creation timestamp
- "Clear all" button with confirmation dialog (calls `DELETE /api/memory`)
- Future: manually add a memory entry via form

**Notes:** Memory is currently keyword-searched in the FileStore. The dashboard search calls the same `SearchMemory` store method through the API.

---

### 3.5 Chat

Talk to the agent directly from the browser. Replaces the need for CLI, Telegram, or Discord during local development or casual use.

**Layout:** Full-height chat panel, message input fixed at the bottom.

**Behavior:**

- Sends user message via WebSocket (`ws://localhost:8080/ws/chat`)
- The Go server injects the message into the agent's inbox as if it came from a `dashboard` channel
- Agent response streams back token-by-token (or in one shot if streaming is not yet implemented)
- Conversation is saved normally to the store under channel ID `dashboard`
- Shows a typing indicator while the agent is processing
- Supports multi-turn: maintains the conversation ID in the WebSocket session

**WebSocket message format:**

```jsonc
// Client → Server (send message)
{ "type": "message", "text": "What files are in ~/workspace?" }

// Server → Client (agent token stream)
{ "type": "token", "text": "Here" }
{ "type": "token", "text": " are" }
{ "type": "done", "conversation_id": "abc123" }

// Server → Client (tool call notification)
{ "type": "tool_start", "name": "shell_exec", "input": "ls ~/workspace" }
{ "type": "tool_done",  "name": "shell_exec", "output": "file1.go\nfile2.go" }

// Server → Client (error)
{ "type": "error", "message": "Provider rate limited" }
```

If streaming is not implemented yet (Phase 1), replace token events with a single `{ "type": "message", "text": "..." }` response.

---

### 3.6 Settings

Configure the agent from the browser without editing YAML files.

The Go server reads the current config on `GET /api/config` and writes an updated file on `PUT /api/config`. The agent reloads config on the next message (or on a signal — TBD).

**Sections within Settings:**

#### Agent
- Agent name (text input)
- System prompt / personality (textarea)
- Max iterations (number, 1–50)
- Max tokens per turn (number)
- History length (number of messages, 1–100)

#### Provider
- Provider type (select: anthropic / openai / ollama / gemini)
- Model name (text input, with a dropdown of known models per provider)
- API key (password input — shown as `••••••••`, editable. Value is written as `${ENV_VAR}` or directly — user chooses)
- Base URL override (text input, optional)
- Timeout (seconds)

#### Channels
- Active channel (select: cli / telegram / discord)
- Telegram token field (shown when telegram selected)
- Telegram allowed user IDs (tag input)
- Discord token (shown when discord selected — Phase 3)

#### Tools
- Per-tool enable/disable toggles (shell, file, http)
- Shell: allowed commands list (tag input), allow_all toggle (with warning)
- File: base path, max file size
- HTTP: timeout, max response size, blocked domains

#### Limits & Budget
- Tool timeout (seconds)
- Total timeout (seconds)
- Monthly cost budget (USD, used for the quota progress bar in Metrics)

#### Dashboard
- Port (number)
- Auth token (password input, optional)

**Save behavior:** Changes are written to the config file immediately. A confirmation toast appears. The agent will use new config on next restart or message (depending on what changed).

---

### 3.7 Logs (optional, Phase 2)

Tail the agent's structured log output in the browser.

- WebSocket stream from `ws://localhost:8080/ws/logs`
- The Go server tees `slog` output to connected WebSocket clients
- Filterable by level (debug / info / warn / error)
- Auto-scroll with a "pause scroll" button
- Max 500 lines in-browser, older lines dropped

---

## 4. API Contract

Base URL: `http://localhost:8080/api`

All responses are JSON. Errors use:
```json
{ "error": "human-readable message" }
```

### 4.1 Agent Status

```
GET /api/status
→ { "status": "running" | "idle" | "error", "uptime_seconds": 3600, "version": "0.1.0" }
```

### 4.2 Metrics

```
GET /api/metrics
→ {
    "today": {
      "input_tokens": 12400,
      "output_tokens": 3200,
      "cost_usd": 0.0412,
      "conversations": 7,
      "messages": 23
    },
    "month": {
      "input_tokens": 284000,
      "output_tokens": 71000,
      "cost_usd": 0.98
    },
    "history": [
      { "date": "2026-03-17", "input_tokens": 12400, "output_tokens": 3200, "cost_usd": 0.0412 },
      ...
    ]
  }
```

```
GET /api/metrics/history?days=30
→ same shape, last N days
```

### 4.3 Conversations

```
GET /api/conversations?limit=20&offset=0&channel=
→ { "items": [Conversation], "total": 142 }

GET /api/conversations/:id
→ Conversation (full messages array)

DELETE /api/conversations/:id
→ 204 No Content
```

`Conversation` shape mirrors `store.Conversation`:

```json
{
  "id": "abc123",
  "channel_id": "cli",
  "messages": [...],
  "created_at": "2026-03-17T10:00:00Z",
  "updated_at": "2026-03-17T10:05:00Z"
}
```

### 4.4 Memory

```
GET /api/memory?q=&limit=20
→ { "items": [MemoryEntry] }

POST /api/memory
body: { "content": "...", "tags": ["tag1"] }
→ 201 MemoryEntry

DELETE /api/memory
→ 204 No Content (clears all entries)

DELETE /api/memory/:id
→ 204 No Content
```

### 4.5 Config

```
GET /api/config
→ Config (full YAML parsed as JSON, API keys masked to "••••••••")

PUT /api/config
body: Config (partial or full — merged with existing)
→ 200 { "message": "config saved, restart may be required" }
```

Sensitive fields (api_key, tokens): the server returns masked values on GET. On PUT, if the value is still masked, it is left unchanged. Only new/changed values are written.

### 4.6 WebSocket — Chat

```
WS /ws/chat

Upgrade with optional header:
  Authorization: Bearer <auth_token>
```

Message protocol: see Section 3.5.

### 4.7 WebSocket — Logs (Phase 2)

```
WS /ws/logs?level=info
```

Each frame is a JSON log line:
```json
{ "time": "2026-03-17T10:00:00Z", "level": "INFO", "msg": "message processed", "channel": "cli" }
```

### 4.8 WebSocket — Metrics Push (optional)

```
WS /ws/metrics
```

Server pushes a metrics snapshot every 30 seconds or when a conversation completes. Same shape as `GET /api/metrics`. Allows the Overview page to update without polling.

---

## 5. Tech Stack

### Frontend

| Concern | Choice | Reason |
|---------|--------|--------|
| Framework | React 18 + TypeScript | Standard, well-documented, no exotic dependencies |
| Build tool | Vite | Fast dev server, simple config, good TS support |
| Routing | React Router v6 | Simple nested routes for the section layout |
| State / data fetching | TanStack Query (React Query) | Handles caching, polling, and refetch on focus automatically |
| WebSocket | Native `WebSocket` API + small custom hook | No need for a library; the protocol is simple |
| Charts + UI | Tremor | Dashboard-first component library. Includes AreaChart, BarChart, DonutChart, stat cards, and progress bars out of the box. Professional look with minimal configuration. Built on Tailwind. |
| Styling | Tailwind CSS | Required by Tremor; utility-first, easy to customize |
| Forms | React Hook Form | Lightweight, pairs with Zod for validation |
| Validation | Zod | Type-safe schema validation for config form |
| Icons | Lucide React | Clean, consistent, tree-shakeable |

**Bundle size target:** Under 400KB gzipped for initial load. Tremor bundles Tailwind — enable purge/content scanning in `tailwind.config.ts` to keep the CSS lean.

### Backend (Go additions)

| Concern | Choice | Reason |
|---------|--------|--------|
| HTTP router | `net/http` stdlib (Go 1.22 `ServeMux` with method+path patterns) | No new dependencies; Go 1.22 patterns cover all needs |
| WebSocket | `golang.org/x/net/websocket` or `nhooyr.io/websocket` | `nhooyr.io/websocket` preferred: context-aware, no CGo, actively maintained |
| CORS | Simple middleware in-house (5 lines) | Only needed for dev; single-origin in production embedding |
| Metrics accumulation | In-process struct with `sync.Mutex` + periodic flush to JSON | Fits the no-external-DB constraint |

---

## 6. Implementation Phases

### Phase 1 — Foundation (build this first)

**Goal:** A working HTTP server and a dashboard that shows live data.

**Backend:**
- [ ] `internal/api/server.go`: start HTTP server, wire to existing Store and Config
- [ ] `GET /api/status` — returns agent status
- [ ] `GET /api/metrics` — returns today's token/cost snapshot (accumulate in memory from `UsageStats` returned by `provider.Chat`)
- [ ] `GET /api/conversations` + `GET /api/conversations/:id` — delegates to `store.ListConversations` and `store.LoadConversation`
- [ ] `GET /api/memory?q=` — delegates to `store.SearchMemory`
- [ ] `GET /api/config` — reads current config, masks sensitive fields
- [ ] Minimal CORS headers for local dev (`Access-Control-Allow-Origin: http://localhost:3000`)

**Frontend:**
- [ ] Vite + React + TypeScript scaffold in `dashboard/`
- [ ] Tailwind + shadcn/ui setup
- [ ] Typed API client (`dashboard/src/api/client.ts`)
- [ ] Overview page: status badge, today's cost/tokens, last activity
- [ ] Conversations list + detail page
- [ ] Memory search page

**Done when:** You can open the browser, see the agent status, browse conversations, and search memory.

---

### Phase 2 — Chat + Config Editor

**Goal:** Replace the CLI with the browser for daily use.

**Backend:**
- [ ] `WS /ws/chat`: WebSocket hub, inject messages into agent inbox via a new `dashboard` channel implementation
- [ ] `PUT /api/config`: validate and write updated config file
- [ ] `DELETE /api/memory` and `DELETE /api/memory/:id`
- [ ] `WS /ws/logs`: tee `slog` output to connected clients

**Frontend:**
- [ ] Chat page: full WebSocket chat UI with typing indicator and tool call display
- [ ] Settings page: full config editor with all sections from Section 3.6
- [ ] Logs page: tailing log viewer with level filter

**Done when:** You can chat with the agent in the browser and change the provider model without opening a terminal.

---

### Phase 3 — Charts + Embedding

**Goal:** Historical data visualization and a single binary for end users.

**Backend:**
- [ ] Persist daily metrics to `~/.microagent/data/metrics.json` (flush on shutdown + every 5 minutes)
- [ ] `GET /api/metrics/history?days=30` endpoint
- [ ] `WS /ws/metrics` push
- [ ] Embed compiled dashboard using `//go:embed` (see below)

**Frontend:**
- [ ] Metrics page: AreaChart (tokens over time), BarChart (cost per day), DonutChart (model breakdown) — all from Tremor
- [ ] Quota ProgressBar in Overview (requires cost budget setting in config)
- [ ] Vite build output configured to `dashboard/dist/` for Go embedding
- [ ] Production mode: API calls use relative URLs (`/api/...`) — no base URL config needed when served from the same origin

**Done when:** `go build` produces a single binary that serves the dashboard at `localhost:8080` with historical charts.

#### How Go embedding works

Go's `//go:embed` directive bundles static files into the binary at compile time. Users need **zero dependencies** — no Node.js, no npm, nothing. Just the binary.

```go
// internal/api/static.go
package api

import "embed"

//go:embed ../../dashboard/dist
var staticFiles embed.FS
```

The HTTP server then serves those files:

```go
// Serve React app for all non-API routes
sub, _ := fs.Sub(staticFiles, "dashboard/dist")
mux.Handle("/", http.FileServer(http.FS(sub)))
```

#### Release build flow

The `Makefile` handles this in two steps:

```makefile
build: build-dashboard build-go

build-dashboard:
	cd dashboard && npm ci && npm run build

build-go:
	go build -o microagent ./cmd/microagent

# Dev mode — no embedding needed
dev-go:
	go run ./cmd/microagent --dashboard

dev-ui:
	cd dashboard && npm run dev
```

CI/GitHub Actions runs `make build` — the resulting `microagent` binary contains the full dashboard.

**Users never need Node.js.** They download (or build) the binary once. That's it.

---

### Phase 4 — Polish (do if and when needed)

- Auth token support (bearer token middleware, UI login screen if token set)
- Discord channel config in Settings (when Discord channel is implemented)
- Manual memory entry creation
- Conversation deletion
- Dark/light mode toggle (Tailwind `dark:` classes)
- Export conversations as JSON or Markdown
- Mobile-responsive layout (the Tailwind base already helps)

---

## 7. Install Script (end-user flow)

The goal is a single command that a user pastes from the README or landing page:

```bash
curl -sSL https://raw.githubusercontent.com/you/microagent/main/install.sh | sh
```

The script does:

1. Detect OS + architecture (`linux/amd64`, `darwin/arm64`, etc.)
2. Download the latest pre-built binary from GitHub Releases
3. Place it at `/usr/local/bin/microagent` (or `~/.local/bin/` if no sudo)
4. Create `~/.microagent/config.yaml` with a commented-out default config if it does not exist
5. Print next steps: "Edit `~/.microagent/config.yaml` and run `microagent`"

**No Go, no Node.js, no Docker required by the user.** The binary is self-contained.

For users who want to build from source:

```bash
git clone https://github.com/you/microagent
cd microagent
make build        # requires Go 1.22+ and Node.js 20+ (dev only)
./microagent
```

---

## Implementation Notes for a Solo Developer

**Start with Phase 1 backend first.** Get `go build` passing with the new `internal/api` package before touching the frontend. Use `curl` to verify endpoints.

**Cost calculation lives client-side.** The Go server tracks raw token counts. The dashboard multiplies by per-model pricing tables hardcoded in the frontend. This avoids having to update the Go binary every time a provider changes pricing.

**WebSocket reconnect logic is mandatory.** The agent may restart. Implement exponential backoff reconnection in the WebSocket hook (`useWebSocket`) from day one.

**No ORM, no migration system.** The metrics JSON file is the only new persistence. Keep it simple: read on startup, update in memory, write on shutdown.

**Config PUT is a full-replace.** Read the current config, merge the incoming changes, write the result. Do not implement partial patch semantics — it adds complexity with little benefit for a local single-user tool.

**Development workflow:**

```bash
# Terminal 1 — Go server
go run ./cmd/microagent --dashboard

# Terminal 2 — React dev server
cd dashboard && npm run dev
# → http://localhost:3000
# Vite proxies /api and /ws to :8080
```

Add to `vite.config.ts`:

```ts
server: {
  proxy: {
    '/api': 'http://localhost:8080',
    '/ws':  { target: 'ws://localhost:8080', ws: true }
  }
}
```
