# Development

Everything you need to build, test, and ship changes to Daimon.

## Build

```bash
make build           # compile binary (TUI-only)
make build-full      # compile with web frontend
make frontend        # download pre-built frontend assets
make copy-frontend   # copy from a local micro-claw-frontend checkout
```

## Test

```bash
make test            # unit tests
make test-race       # unit tests with race detector
make lint            # golangci-lint
make ci              # vet + lint + test-race (run this before pushing)
```

## Project structure

```
cmd/microagent/       entrypoint, subcommands
internal/
  agent/              agent loop, context builder
  channel/            CLI, Telegram, Discord, WhatsApp, Web
  provider/           OpenRouter, Anthropic, Gemini, OpenAI, Ollama
  tool/               shell, file, HTTP, MCP tools
  store/              SQLite persistence
  web/                HTTP server, REST API, WebSocket, auth
  mcp/                MCP client (stdio + http)
  cron/               scheduler, daemon mode
  skill/              skill loader, parser
  config/             YAML config, validation
  audit/              audit log
configs/              example config + skill files
```

For the full architecture breakdown, see `MICROAGENT.md` in the repo root.

## Running locally

```bash
# Agent with CLI channel
./bin/microagent

# Agent with web dashboard
./bin/microagent web

# Re-run the setup wizard
./bin/microagent --setup
```

Config search order: `--config` flag → `~/.microagent/config.yaml` →
`./config.yaml`.

## CLI reference

```bash
microagent                            # start the agent (setup wizard if no config)
microagent --web                      # start with web dashboard
microagent --dashboard                # TUI dashboard (read-only)
microagent --setup                    # re-run setup wizard
microagent --daemon                   # cron-only background mode

microagent web [--port N] [--host H]  # web-only mode
microagent setup                      # setup wizard
microagent doctor                     # validate config
microagent config                     # show active config

microagent mcp list|add|remove|test|validate|manage
microagent skills add|list|info|remove
microagent cron list|info|delete
```

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full workflow.
