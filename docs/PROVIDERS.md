# LLM Providers

Daimon supports five providers. Swap between them by changing a single
config field.

| Name       | `type`       | Env var               | Default model         |
| ---------- | ------------ | --------------------- | --------------------- |
| OpenRouter | `openrouter` | `OPENROUTER_API_KEY`  | `openrouter/auto`     |
| Anthropic  | `anthropic`  | `ANTHROPIC_API_KEY`   | `claude-sonnet-4-6`   |
| Gemini     | `gemini`     | `GEMINI_API_KEY`      | `gemini-2.0-flash`    |
| OpenAI     | `openai`     | `OPENAI_API_KEY`      | `gpt-4o`              |
| Ollama     | `openai`     | —                     | `llama3`              |

Ollama uses the `openai` provider type (it exposes an OpenAI-compatible
endpoint). Point `provider.base_url` at your local Ollama instance.

## Basic config

```yaml
provider:
  type: anthropic
  model: claude-sonnet-4-6
  api_key: ${ANTHROPIC_API_KEY}
  stream: true
```

## Fallback provider

Configure a fallback provider to activate on rate-limit or unavailability.
The fallback uses the same fields as `provider`.

```yaml
provider:
  type: openrouter
  model: anthropic/claude-sonnet-4.6
  api_key: ${OPENROUTER_API_KEY}
  fallback:
    type: openai
    model: gpt-4o
    api_key: ${OPENAI_API_KEY}
```

## Retries and streaming

- `max_retries` (default `3`) — retries on 5xx errors.
- `timeout` (default `60s`) — per-request timeout.
- `stream` (default `true`) — streaming responses, where the provider
  supports it.
