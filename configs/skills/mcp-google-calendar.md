---
name: mcp-google-calendar
description: Instructions for using Google Calendar MCP tools
version: 2.0.0
---

You have access to Google Calendar via MCP tools.

## CRITICAL: Authentication MUST come first

Google Calendar requires OAuth authentication before any data tools become available.

**Before doing ANYTHING with Google Calendar**, check if data tools (like listing events) are available:
- If only `authenticate` and `complete_authentication` tools are available, you MUST authenticate first.
- If data tools (list events, create events, etc.) are already available, skip to "Usage" below.

### Authentication flow

1. Call the Google Calendar `authenticate` tool (no parameters needed).
2. You will receive an authorization URL. Share it with the user and ask them to open it in their browser.
3. After the user authorizes, their browser redirects to a `http://localhost:<port>/callback?code=...&state=...` URL. Ask the user to copy the full URL from their browser's address bar and paste it back.
4. Call `complete_authentication` with the full callback URL the user provided.
5. After successful authentication, the Calendar data tools become available. Proceed with the user's request.

**Important**: If the user asks to do anything with Google Calendar and authentication hasn't been completed, ALWAYS start with step 1. Do NOT attempt to call data tools that don't exist yet — they will fail silently.

## Usage (after authentication)

### Guidelines

1. **Time zones**: Always use the user's configured timezone when creating or displaying events.
2. **Confirm before creating**: Always show the event details and ask for confirmation before creating events.
3. **Be concise**: When listing events, show: title, date/time, duration, and location (if any).
4. **Default range**: When asked "what's on my calendar", default to today + next 7 days.

### Common tasks

- **"What's on my calendar today?"** → List events for today
- **"Schedule a meeting"** → Ask for title, date, time, duration, then create
- **"Am I free tomorrow at 3pm?"** → Check free/busy for that time slot
- **"Show my week"** → List events for the next 7 days
