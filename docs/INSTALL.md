# Install

Daimon ships as a single binary with zero runtime dependencies. Pick the
install path that fits your environment.

## Option A — One-liner (recommended)

Detects your OS and architecture, downloads the latest release, and installs
to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/mmmarxdr/micro-claw/main/install.sh | sh
```

## Option B — Download a release manually

Grab the binary for your platform from [Releases](https://github.com/mmmarxdr/micro-claw/releases).
Release archives include the web frontend.

```bash
# Linux (amd64)
tar -xzf microagent_*_linux_amd64.tar.gz
chmod +x microagent
sudo mv microagent /usr/local/bin/
```

## Option C — Build from source

```bash
git clone https://github.com/mmmarxdr/micro-claw.git
cd micro-claw

# TUI-only (no Node.js needed)
make build

# With web frontend (downloads pre-built assets, no Node.js needed)
make build-full

# Binary lands at bin/microagent
```

## Option D — `go install`

```bash
go install github.com/mmmarxdr/micro-claw/cmd/microagent@latest
```

> Note: `go install` builds without the web frontend. The TUI and API
> still work. To add the frontend, see
> [docs/WEB_DASHBOARD.md](WEB_DASHBOARD.md).

## First run

```bash
microagent web
```

On first run with no config, the browser-based setup wizard launches
automatically. It walks you through provider, API key and model, validates
the key with a real API call, and writes `~/.microagent/config.yaml`.

Alternative TUI wizard:

```bash
microagent --setup
```

Manual config: see [docs/CONFIG.md](CONFIG.md).
