# Notifications

Daimon has an internal event bus and a rules engine that delivers push
notifications when something happens — a cron job finished, an agent turn
completed, a task failed. Notifications ship to any configured channel:
Telegram, Discord, WhatsApp, the web dashboard, or via an MCP integration.

## How it works

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

1. Internal events are emitted to an event bus (non-blocking, buffered).
2. The rules engine evaluates each event against configured rules.
3. Matching rules render a Go template and send through the target channel.
4. If delivery fails, an optional fallback channel is tried.
5. Every notification is audited (visible in the Logs dashboard).

## Event types

| Event                   | When it fires                                | Useful for                     |
| ----------------------- | -------------------------------------------- | ------------------------------ |
| `cron.job.fired`        | A cron job starts executing                  | Tracking execution             |
| `cron.job.completed`    | A cron job finished successfully             | Results on another channel     |
| `cron.job.failed`       | A cron job errored                           | Failure alerts                 |
| `agent.turn.started`    | The agent begins processing a message        | Activity monitoring            |
| `agent.turn.completed`  | The agent finished responding                | Task completion alerts         |

## Configuration

```yaml
notifications:
  enabled: true
  max_per_minute: 30          # circuit breaker
  bus_buffer_size: 256        # internal event queue size
  handler_timeout_sec: 5      # max seconds to wait for channel delivery
  rules:
    - name: cron-results
      event_type: cron.job.completed
      target_channel: "telegram:7535164458"
      template: "{{.JobPrompt}}: {{.Text}}"
      cooldown_sec: 10

    - name: cron-failures
      event_type: cron.job.failed
      target_channel: "telegram:7535164458"
      fallback_channel: "web:broadcast"
      template: "Task '{{.JobPrompt}}' failed: {{.Error}}"

    - name: job-specific
      event_type: cron.job.completed
      job_id: "a22ea2c4-..."
      target_channel: "telegram:7535164458"
      template: "Report ready: {{.Text}}"

    - name: turn-done
      event_type: agent.turn.completed
      target_channel: "web:broadcast"
      template: "Agent done — {{.Meta.input_tokens}} in / {{.Meta.output_tokens}} out"
      cooldown_sec: 60
```

## Template variables

Templates use Go's `text/template` syntax. Fields available on every event:

| Field                       | Type      | Description                                   |
| --------------------------- | --------- | --------------------------------------------- |
| `{{.Type}}`                 | string    | Event type (e.g. `cron.job.completed`)        |
| `{{.JobID}}`                | string    | Cron job ID (empty for agent events)          |
| `{{.JobPrompt}}`            | string    | The cron job's prompt text                    |
| `{{.ChannelID}}`            | string    | Originating channel                           |
| `{{.Text}}`                 | string    | Result text (agent response or cron output)   |
| `{{.Error}}`                | string    | Error message (only on failures)              |
| `{{.Timestamp}}`            | time.Time | When the event occurred                       |
| `{{.Meta.input_tokens}}`    | string    | Input tokens (turn events only)               |
| `{{.Meta.output_tokens}}`   | string    | Output tokens (turn events only)              |

## Channel ID format

Target channels use `<channel_type>:<identifier>`:

| Channel  | Format                | Example                    |
| -------- | --------------------- | -------------------------- |
| Telegram | `telegram:<chat_id>`  | `telegram:7535164458`      |
| Discord  | `discord:<channel>`   | `discord:123456789`        |
| WhatsApp | `whatsapp:<phone>`    | `whatsapp:+5491155551234`  |
| Web      | `web:broadcast`       | All connected web clients  |

## Safety features

| Feature              | Description                                                       |
| -------------------- | ----------------------------------------------------------------- |
| Loop prevention      | Notification events never trigger other rules                     |
| Circuit breaker      | `max_per_minute` caps total notifications globally (default: 30)  |
| Per-rule cooldown    | `cooldown_sec` prevents the same rule from firing too frequently  |
| Fallback channel     | If primary delivery fails, tries `fallback_channel`               |
| Audit trail          | Every notification (sent or failed) recorded as an audit event    |
| Non-blocking         | Event bus never blocks the agent — events drop if buffer is full  |
| Startup validation   | Rules are validated on startup (templates, names, event types)    |

## Examples

**Daily calendar summary to Telegram**

1. Ask the agent: *"Every day at 9am tell me my calendar events for today."*
2. Configure a notification rule:

```yaml
- name: daily-calendar
  event_type: cron.job.completed
  target_channel: telegram:<your_chat_id>
  template: "{{.Text}}"
```

**Alert on cron failures**

```yaml
- name: alert-failures
  event_type: cron.job.failed
  target_channel: "telegram:7535164458"
  template: "Scheduled task failed. Task: {{.JobPrompt}} Error: {{.Error}}"
```

**Weekly email summary via Gmail MCP**

Ask the agent: *"Every Monday at 8am, summarize my unread emails from the
past week and send the summary to marxdr7@gmail.com using Gmail."*

The cron job's prompt includes the "send email" instruction, so the agent
uses the Gmail MCP tool during execution.
