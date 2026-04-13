---
name: mcp-gmail
description: Instructions for efficiently using Gmail MCP tools
version: 2.0.0
---

You have access to Gmail via MCP tools. Follow these rules:

## CRITICAL: Authentication MUST come first

Gmail requires OAuth authentication before any data tools become available.

**Before doing ANYTHING with Gmail**, check if data tools (like listing or searching emails) are available:
- If only `authenticate` and `complete_authentication` tools are available, you MUST authenticate first.
- If data tools (search, list, send, etc.) are already available, skip to "Usage" below.

### Authentication flow

1. Call the Gmail `authenticate` tool (no parameters needed).
2. You will receive an authorization URL. Share it with the user and ask them to open it in their browser.
3. After the user authorizes, their browser redirects to a `http://localhost:<port>/callback?code=...&state=...` URL. Ask the user to copy the full URL from their browser's address bar and paste it back.
4. Call `complete_authentication` with the full callback URL the user provided.
5. After successful authentication, the Gmail data tools become available. Proceed with the user's request.

**Important**: If the user asks to do anything with Gmail and authentication hasn't been completed, ALWAYS start with step 1. Do NOT attempt to call data tools that don't exist yet — they will fail silently.

## Usage (after authentication)

### Minimize data fetched

Email responses can be very large (100KB+ per email with HTML bodies). Always:

1. **Start with counts**: Check how many messages exist before fetching content.
2. **Use small limits**: When fetching messages, always set `limit` to 5 or less.
3. **Search first**: Prefer search tools over listing all messages to get targeted results.
4. **Summarize, don't dump**: Show only: sender, subject, date, and a 1-2 line summary. Never paste the full email body unless explicitly asked.

### Common tasks

- **"How many emails do I have?"** → Get message count
- **"Show my unread emails"** → Get unseen messages with `limit: 5`
- **"Search emails from X"** → Search by sender
- **"Search emails about X"** → Search by subject
- **"Send an email"** → Use send with `to`, `subject`, and `text` fields
- **"Reply to email"** → First get the message UID, then reply
