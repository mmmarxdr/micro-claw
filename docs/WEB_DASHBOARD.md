# Web Dashboard

The web dashboard gives you a browser-based UI with real-time chat, metrics,
conversation history, and config management.

## Start the dashboard

```bash
# Standalone (web is the only interface)
microagent web

# Alongside CLI/Telegram (both work simultaneously)
microagent --web
```

On startup, the agent generates an auth token (if none is configured) and
prints it to the console:

```
INFO web dashboard available url=http://127.0.0.1:8080 auth_token=a1b2c3d4...
```

Open the URL in your browser, enter the token, and you are in.

## Auth token options

The dashboard is **always authenticated**. Three ways to set the token:

| Method                | Example                                                      |
| --------------------- | ------------------------------------------------------------ |
| Config file           | `web.auth_token: "my-secret-token"`                          |
| Environment variable  | `MICROAGENT_WEB_TOKEN=my-secret-token microagent web`        |
| Auto-generated        | Leave empty — token is printed on startup                    |

For production or VPS deployments, set a fixed token in config or env so
it persists across restarts. See [docs/DEPLOY.md](DEPLOY.md).

## Config

```yaml
web:
  enabled: true
  port: 8080
  host: "127.0.0.1"    # 0.0.0.0 to expose to network
  auth_token: ""        # auto-generated if empty
```

## API endpoints

All `/api/*` and `/ws/*` endpoints require
`Authorization: Bearer <token>`. WebSocket endpoints accept
`?token=<token>` as an alternative.

| Endpoint                        | Method     | Description                        |
| ------------------------------- | ---------- | ---------------------------------- |
| `/api/status`                   | GET        | Agent status, uptime, version      |
| `/api/config`                   | GET        | Active config (secrets masked)     |
| `/api/conversations`            | GET        | List conversations                 |
| `/api/conversations/{id}`       | GET/DELETE | Get or delete a conversation       |
| `/api/memory`                   | GET/POST   | List or create memory entries      |
| `/api/memory/{id}`              | DELETE     | Delete a memory entry              |
| `/api/metrics`                  | GET        | Token usage and cost metrics       |
| `/api/mcp/servers`              | GET        | MCP server status                  |
| `/ws/chat`                      | WS         | Real-time chat with the agent      |
| `/ws/metrics`                   | WS         | Live metrics stream                |
| `/ws/logs`                      | WS         | Live audit log stream              |
