# Deploy on a VPS

This guide covers running Daimon on a Linux server with the web dashboard
accessible remotely over HTTPS.

## 1. Install

```bash
curl -sL https://github.com/mmmarxdr/micro-claw/releases/latest/download/microagent_linux_amd64.tar.gz | tar -xz
sudo mv microagent /usr/local/bin/
```

## 2. Configure

```bash
mkdir -p ~/.microagent
cat > ~/.microagent/config.yaml << 'EOF'
agent:
  name: "Micro"
  personality: "You are a helpful assistant."

provider:
  type: openrouter
  model: google/gemini-2.0-flash-001
  api_key: ${OPENROUTER_API_KEY}
  stream: true

channel:
  type: cli

store:
  type: sqlite
  path: "~/.microagent/data"

web:
  enabled: true
  port: 8080
  host: "127.0.0.1"           # keep localhost — Caddy handles external traffic
  auth_token: ${MICROAGENT_WEB_TOKEN}

audit:
  enabled: true
  path: "~/.microagent/audit"
EOF
```

## 3. Set secrets

```bash
# Add to ~/.bashrc or use a secrets manager
export OPENROUTER_API_KEY="sk-or-v1-..."
export MICROAGENT_WEB_TOKEN="$(openssl rand -hex 32)"
echo "Your web token: $MICROAGENT_WEB_TOKEN"
```

## 4. Reverse proxy with HTTPS (Caddy)

```bash
sudo apt install caddy
```

```
# /etc/caddy/Caddyfile
agent.yourdomain.com {
    reverse_proxy localhost:8080
}
```

```bash
sudo systemctl reload caddy
```

Caddy automatically provisions a Let's Encrypt TLS certificate. Your
dashboard is now at `https://agent.yourdomain.com`.

## 5. Run as a systemd service

```ini
# /etc/systemd/system/microagent.service
[Unit]
Description=Daimon (microagent) AI agent
After=network.target

[Service]
Type=simple
User=microagent
Environment=OPENROUTER_API_KEY=sk-or-v1-...
Environment=MICROAGENT_WEB_TOKEN=your-fixed-token-here
ExecStart=/usr/local/bin/microagent web
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /bin/false microagent
sudo systemctl daemon-reload
sudo systemctl enable --now microagent
```

## 6. Verify

```bash
# Check the service
sudo systemctl status microagent

# Test the API
curl -H "Authorization: Bearer $MICROAGENT_WEB_TOKEN" \
  https://agent.yourdomain.com/api/status
```
