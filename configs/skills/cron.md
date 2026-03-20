---
name: cron_scheduler
description: Enables the agent to schedule recurring tasks using natural language
version: 1.0.0
author: microagent
---

## Scheduling Tasks

You have the ability to schedule tasks to run automatically at specific times or intervals. When a user asks to schedule something recurring or future, use the `schedule_task` tool.

### When to use `schedule_task`

Trigger `schedule_task` when the user:
- Uses explicit keywords: `/cron`, "schedule", "every day", "every morning", "remind me", "weekly", "monthly", "at [time]", "automatically"
- Asks for recurring reports, summaries, or actions
- Wants something done at a future time

### How to call `schedule_task`

Parameters:
- `schedule`: The timing in natural language OR a valid cron expression. Examples: "every day at 9am", "every Monday at 8:00", "0 9 * * 1-5"
- `prompt`: The exact task the agent should perform when it runs. Be specific and self-contained — the agent will run this prompt independently without conversation context.
- `channel_id`: Use the current channel ID from the conversation.

### Writing effective scheduled prompts

Since scheduled tasks run without conversation context, the prompt must be self-contained:
- Good: "Fetch and summarize the top 5 unread emails from the last 24 hours and list them with sender, subject, and one-line summary"
- Bad: "Give me the report" (too vague — no context when it runs)

### After scheduling

When `schedule_task` succeeds:
1. Confirm the schedule in plain language: "I've scheduled this to run every morning at 10am"
2. Show the job ID for future reference
3. If there are missing tool warnings, explain what needs to be configured and how

### Managing scheduled tasks

- To see all scheduled tasks: call `list_crons`
- To remove a task: call `delete_cron` with the job ID
- If the user asks "what's scheduled?" or "show my cron jobs" — call `list_crons`
