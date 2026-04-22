# Install

Daimon ships as a single binary with zero runtime dependencies. Pick the
install path that fits your environment.

## Option A — One-liner (recommended)

Detects your OS and architecture, downloads the latest release, and installs
to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/mmmarxdr/daimon/main/install.sh | sh
```

## Option B — Download a release manually

Grab the binary for your platform from [Releases](https://github.com/mmmarxdr/daimon/releases).
Release archives include the web frontend.

```bash
# Linux (amd64)
tar -xzf daimon_*_linux_amd64.tar.gz
chmod +x daimon
sudo mv daimon /usr/local/bin/
```

## Option C — Build from source

```bash
git clone https://github.com/mmmarxdr/daimon.git
cd daimon

# TUI-only (no Node.js needed)
make build

# With web frontend (downloads pre-built assets, no Node.js needed)
make build-full

# Binary lands at bin/daimon
```

## Option D — `go install`

```bash
go install github.com/mmmarxdr/daimon/cmd/daimon@latest
```

> Note: `go install` builds without the web frontend. The TUI and API
> still work. To add the frontend, see
> [docs/WEB_DASHBOARD.md](WEB_DASHBOARD.md).

## Updating

Once installed (any path that produces a versioned binary — Options A, B, or C
with goreleaser), Daimon can update itself from the latest GitHub release:

```bash
daimon update              # download and install the latest release
daimon update --check      # only report whether a newer version exists
daimon update --version v0.7.0   # rollback or pin to a specific tag
daimon update --force      # reinstall even if already on the latest version
```

`daimon update` writes to the same path that the running binary lives at
(`os.Executable()`), so it works regardless of where you installed it. If the
target directory requires elevated permissions (e.g. `/usr/local/bin`),
re-run with `sudo`.

> Note: builds produced by plain `go build` or `go install` (Option D) carry
> no version metadata and will refuse self-update — use `go install
> github.com/mmmarxdr/daimon/cmd/daimon@latest` instead, or pass an explicit
> `--version vX.Y.Z` to opt into self-replacement.

## First run

```bash
daimon web
```

On first run with no config, the browser-based setup wizard launches
automatically. It walks you through provider, API key and model, validates
the key with a real API call, and writes `~/.daimon/config.yaml`.

Alternative TUI wizard:

```bash
daimon --setup
```

Manual config: see [docs/CONFIG.md](CONFIG.md).
