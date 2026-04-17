# MCP Integrations

MCP (Model Context Protocol) lets the agent connect to external services
at runtime — no code changes required. MCP servers run as subprocesses
(stdio) or HTTP endpoints, expose tools, and Daimon registers them
alongside its built-ins.

> **Requirements.** Most MCP servers need **Node.js >= 18** (for `npx`).
> Some use Python. Daimon itself does not require Node.

## How it works

1. You add an MCP server to your config under `tools.mcp.servers[]`.
2. On startup, Daimon connects to each server and discovers its tools.
3. Tools are registered alongside the built-ins — the LLM can call them
   like any other tool.
4. Use `prefix_tools: true` to namespace tool names when you have multiple
   servers configured.

## Auth patterns

There are two patterns depending on the server:

| Pattern    | How it works                                                       | Examples                        |
| ---------- | ------------------------------------------------------------------ | ------------------------------- |
| Env-based  | Credentials in env vars, server auto-connects                      | Gmail (IMAP), GitHub, Brave     |
| OAuth      | Server exposes an `authenticate` tool; user authorizes in browser  | Google Calendar, Workspace      |

## Setup: Gmail (env-based)

1. Enable [2-Step Verification](https://myaccount.google.com/security)
   on your Google account.
2. Generate an [App Password](https://myaccount.google.com/apppasswords)
   (name it "daimon").
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

4. Verify the connection: `microagent mcp test gmail`
5. Ask the agent: *"Show me my unread emails."*

## Setup: Google Calendar (OAuth)

OAuth servers require a one-time browser authorization before they work.

**Step 1 — Google Cloud credentials** (one-time):

1. Create a project in the [Google Cloud Console](https://console.cloud.google.com).
2. Enable **Google Calendar API** (APIs & Services > Library).
3. Create OAuth credentials (Desktop app).
4. Download the JSON and save it to `~/.microagent/google-credentials.json`.

**Step 2 — Authorize** (one-time, run outside Daimon):

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
      - name: google-calendar
        transport: stdio
        command: ["npx", "-y", "@cocal/google-calendar-mcp"]
        prefix_tools: true
        env:
          GOOGLE_OAUTH_CREDENTIALS: "/home/you/.microagent/google-credentials.json"
```

Restart and ask: *"What events do I have this week?"*

## Popular integrations

| Server              | Install                                                | Auth                    | What it does                          |
| ------------------- | ------------------------------------------------------ | ----------------------- | ------------------------------------- |
| Gmail (IMAP)        | `npx -y mcp-mail-server`                               | Env (app password)      | Read, send, search, reply to emails   |
| Google Calendar     | `npx -y @cocal/google-calendar-mcp`                    | OAuth (browser)         | Events, scheduling, free/busy         |
| Google Workspace    | `pip install workspace-mcp`                            | OAuth (browser)         | Gmail + Calendar + Drive + Docs       |
| GitHub              | `npx -y @modelcontextprotocol/server-github`           | Env (`GITHUB_TOKEN`)    | Issues, PRs, repos, CI                |
| Filesystem          | `npx -y @modelcontextprotocol/server-filesystem /path` | None                    | Sandboxed file access                 |
| Brave Search        | `npx -y @modelcontextprotocol/server-brave-search`     | Env (`BRAVE_API_KEY`)   | Web search                            |
| Notion              | `npx -y @notionhq/notion-mcp-server`                   | Env (`NOTION_TOKEN`)    | Pages, databases, blocks              |
| Slack               | `npx -y @modelcontextprotocol/server-slack`            | Env (`SLACK_BOT_TOKEN`) | Channels, messages, threads           |

## Auto-installed skills

When you add an MCP server from the catalog (via CLI or web dashboard),
Daimon automatically installs a **skill file** that teaches the LLM how to
use that integration efficiently. Skills include:

- Token-saving strategies (fetch counts before content, use small limits).
- Auth flow instructions (for OAuth servers).
- Common task mappings ("show my unread emails" → which tool to call).

Skills are loaded on demand via the `load_skill` tool — they do not waste
context tokens on every message. See [docs/SKILLS.md](SKILLS.md).

## Managing MCP servers

```bash
microagent mcp list                # show configured servers
microagent mcp test <name>         # live connection test
microagent mcp add --name X --transport stdio --command "npx ..."
microagent mcp remove <name>
microagent mcp validate            # check config + env vars
```

Or manage from the web dashboard: **Integrations** page (add from catalog,
test, remove).
