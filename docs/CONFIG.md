# Configuration Reference

Daimon reads its config from `~/.microagent/config.yaml` by default. The
search order is:

1. `--config` CLI flag
2. `~/.microagent/config.yaml`
3. `./config.yaml`

All string values support `${ENV_VAR}` interpolation. Paths support `~`
expansion.

## Minimal config

```yaml
agent:
  name: "Micro"
  personality: "You are a concise, helpful assistant."

provider:
  type: openrouter
  model: openrouter/auto
  api_key: "sk-or-v1-..."   # or ${OPENROUTER_API_KEY}

channel:
  type: cli

store:
  type: sqlite
  path: "~/.microagent/data"
```

## `agent`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `name` | string | `"Micro"` | Agent name |
| `personality` | string | — | System prompt injected at every turn |
| `max_iterations` | int | `10` | Max tool-use cycles per message |
| `max_tokens_per_turn` | int | `4096` | Max tokens per LLM call |
| `history_length` | int | `20` | Conversation messages kept in context |
| `memory_results` | int | `5` | Max memory entries injected into context |

## `provider`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `type` | string | — | `openrouter`, `anthropic`, `gemini`, `openai`, `ollama` |
| `model` | string | provider default | Model identifier |
| `api_key` | string | — | API key (supports `${ENV_VAR}`) |
| `timeout` | duration | `60s` | Per-request timeout |
| `max_retries` | int | `3` | Retries on 5xx errors |
| `stream` | bool | `true` | Enable streaming responses |
| `fallback` | object | — | Fallback provider (same fields) — activates on rate-limit or unavailability |

See [docs/PROVIDERS.md](PROVIDERS.md) for the provider table.

## `channel`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `type` | string | — | `cli`, `telegram`, `discord`, `whatsapp` |
| `token` | string | — | Bot API token (Telegram/Discord) |
| `allowed_users` | []int64 | — | User ID whitelist |

See [docs/CHANNELS.md](CHANNELS.md) for per-channel setup.

## `web`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `enabled` | bool | `false` | Enable web dashboard (also via `--web` flag) |
| `port` | int | `8080` | HTTP port |
| `host` | string | `"127.0.0.1"` | Bind address (`0.0.0.0` to expose) |
| `auth_token` | string | auto-generated | Bearer token; also `MICROAGENT_WEB_TOKEN` env var |

See [docs/WEB_DASHBOARD.md](WEB_DASHBOARD.md) for the full web guide.

## `tools.shell`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `enabled` | bool | `true` | Enable `shell_exec` tool |
| `allowed_commands` | []string | `[ls, cat, ...]` | Whitelisted commands |
| `allow_all` | bool | `false` | Allow any command |
| `working_dir` | string | `"~"` | Working directory |

## `tools.file`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `enabled` | bool | `true` | Enable file tools |
| `base_path` | string | `"~/workspace"` | Sandbox root |
| `max_file_size` | string | `"1MB"` | Max file size |

## `tools.http` / `tools.web_fetch`

See [docs/TOOLS.md](TOOLS.md).

## `tools.mcp`

See [docs/MCP.md](MCP.md).

## `store`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `type` | string | `"file"` | `file` or `sqlite` |
| `path` | string | `"~/.microagent/data"` | Storage directory |

## `logging`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `level` | string | `"info"` | `debug`, `info`, `warn`, `error` |
| `format` | string | `"text"` | `text` or `json` |

## `limits`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `tool_timeout` | duration | `30s` | Max time per tool execution |
| `total_timeout` | duration | `120s` | Max time per agent turn |

## `audit`

| Field | Type | Default | Description |
| ----- | ---- | ------- | ----------- |
| `enabled` | bool | `false` | Enable audit log |
| `path` | string | `"~/.microagent/audit"` | Audit log directory |

## `cron`

See [docs/CRON.md](CRON.md).

## `skills`

See [docs/SKILLS.md](SKILLS.md).

## `notifications`

See [docs/NOTIFICATIONS.md](NOTIFICATIONS.md).
