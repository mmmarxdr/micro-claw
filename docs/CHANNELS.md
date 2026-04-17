# Channels

A channel is how users talk to the agent. Daimon ships with four: a local
CLI, Telegram, Discord, and WhatsApp. The web dashboard is a fifth interface
and is documented separately in [docs/WEB_DASHBOARD.md](WEB_DASHBOARD.md).

| Name     | `type`     | Required fields                                                 | Notes                                       |
| -------- | ---------- | --------------------------------------------------------------- | ------------------------------------------- |
| CLI      | `cli`      | None                                                            | Reads stdin, writes stdout                  |
| Telegram | `telegram` | `token`, `allowed_users`                                        | Bot API token from @BotFather               |
| Discord  | `discord`  | `token`, `allowed_users`                                        | Discord bot via WebSocket gateway           |
| WhatsApp | `whatsapp` | `token`, `verify_token`, `phone_number_id`, `allowed_phones`    | WhatsApp Cloud API webhook                  |

## CLI

```yaml
channel:
  type: cli
```

Run `microagent` and start typing. The agent responds inline.

## Telegram

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy the token.
2. Start your bot once in a direct message so Telegram registers the chat.
3. Find your user ID (any `userinfobot` will do).

```yaml
channel:
  type: telegram
  token: ${TELEGRAM_BOT_TOKEN}
  allowed_users: [7535164458]
```

## Discord

1. Create a Discord application in the [developer portal](https://discord.com/developers/applications)
   and add a bot user.
2. Enable `MESSAGE CONTENT INTENT` in the bot settings.
3. Invite the bot to your server with appropriate scopes.

```yaml
channel:
  type: discord
  token: ${DISCORD_BOT_TOKEN}
  allowed_users: [123456789012345678]
```

## WhatsApp

Uses the WhatsApp Cloud API. You need a Meta developer account, a
registered phone number, and a public webhook endpoint.

```yaml
channel:
  type: whatsapp
  token: ${WHATSAPP_TOKEN}
  verify_token: ${WHATSAPP_VERIFY_TOKEN}
  phone_number_id: "123456789"
  allowed_phones: ["+5491155551234"]
```

Daimon exposes the webhook at `/api/webhook/whatsapp` on the web server.
When deploying, point your Cloud API webhook URL at it.
