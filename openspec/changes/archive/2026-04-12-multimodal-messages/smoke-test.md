# Smoke Test: Multimodal Messages (Telegram)

## Prerequisites
- A Telegram bot token configured in `config.yaml`
- A Telegram chat with the bot (whitelisted user)
- `media.enabled: true` in config

## Steps
1. Send a text message → verify normal response
2. Send a photo with caption → verify agent acknowledges the image
3. Send a voice note → verify agent acknowledges the audio
4. Send an oversized file (>10MB) → verify rejection notice
5. Send a PDF document → verify agent acknowledges the document
6. Set `media.enabled: false` → send a photo → verify disabled notice
7. Use FileStore backend → send a photo → verify media not supported warning at startup
