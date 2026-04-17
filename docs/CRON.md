# Scheduled Tasks (Cron)

Schedule recurring tasks in natural language. The agent translates a
natural-language description to a cron expression, executes the prompt
autonomously at each tick, and delivers results to the channel where the
task was created.

> **Requirements.** `store.type: sqlite` and `cron.enabled: true`.

## Config

```yaml
cron:
  enabled: true
  timezone: America/Argentina/Buenos_Aires   # optional, defaults to UTC
  notify_on_completion: true                 # prefix results with task info
  retention_days: 30                         # delete results older than N days
  max_concurrent: 4                          # max concurrent agent turns
```

## Creating tasks

Just ask the agent:

- *"Every morning at 9am give me a summary of my calendar events."*
- *"Every minute tell me the current time."*
- *"Every Monday at 8am send me a weekly report of my emails."*

The agent uses the `schedule_task` tool automatically. Prompts must be
self-contained — the agent runs them without conversation context, so
include all relevant details in the prompt itself.

## Managing tasks

| Method | How                                                         |
| ------ | ----------------------------------------------------------- |
| Chat   | *"Show my scheduled tasks"* → agent calls `list_crons`      |
| Chat   | *"Cancel task X"* → agent calls `delete_cron`               |
| CLI    | `microagent cron list` / `microagent cron delete <id>`      |
| Daemon | `microagent --daemon` — cron-only, no interactive channel   |

## Per-job notification

Each cron job can override where its completion notification goes via
`notify_channel` and `notify_on_completion`. See
[docs/NOTIFICATIONS.md](NOTIFICATIONS.md).
