#!/usr/bin/env bash
# test.sh — Contributor verification script for microagent
#
# Runs all checks required to validate a working build:
#   1. Go toolchain present
#   2. Project compiles cleanly
#   3. Unit tests pass
#   4. Integration tests pass (compiles a helper MCP server at test startup)
#
# Usage: ./test.sh
# No arguments needed. Intended for contributors and CI.
# Exits immediately on first failure (set -e).

set -euo pipefail

GREEN='\033[0;32m'
NC='\033[0m'  # no colour

# ----------------------------------------------------------------------------
# 1. Go toolchain
# ----------------------------------------------------------------------------
echo "==> Checking Go toolchain..."
if ! command -v go &>/dev/null; then
  echo "ERROR: 'go' not found on PATH. Install Go 1.22+ from https://go.dev/dl/" >&2
  exit 1
fi
go version

# ----------------------------------------------------------------------------
# 2. Build
# ----------------------------------------------------------------------------
echo "==> Building..."
go build -ldflags="-s -w" -o /tmp/microagent-test-build ./cmd/microagent
rm -f /tmp/microagent-test-build

# ----------------------------------------------------------------------------
# 3. Unit tests
# ----------------------------------------------------------------------------
echo "==> Running unit tests..."
go test ./... -timeout 60s

# ----------------------------------------------------------------------------
# 4. Integration tests
# ----------------------------------------------------------------------------
echo "==> Running integration tests..."
go test -tags=integration ./internal/mcp/... -v -timeout 60s

# ----------------------------------------------------------------------------
# Done
# ----------------------------------------------------------------------------
echo -e "${GREEN}==> All checks passed!${NC}"
