VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

FRONTEND_DIR  ?= ../micro-claw-frontend
FRONTEND_REPO ?= mmmarxdr/micro-claw-frontend
FRONTEND_TAG  ?= latest
STATIC_DIR    := internal/web/static

.PHONY: build build-full test test-race integration-test vet lint fmt ci docker-build clean dev-run copy-frontend frontend

# ---------------------------------------------------------------------------
# Frontend
# ---------------------------------------------------------------------------

# Download pre-built frontend from GitHub Releases (no Node.js required).
frontend:
	@echo "Downloading frontend from $(FRONTEND_REPO)@$(FRONTEND_TAG)..."
	@mkdir -p $(STATIC_DIR)/assets
	@if [ "$(FRONTEND_TAG)" = "latest" ]; then \
		curl -sfL "https://github.com/$(FRONTEND_REPO)/releases/latest/download/frontend-dist.tar.gz" \
			| tar -xz -C $(STATIC_DIR); \
	else \
		curl -sfL "https://github.com/$(FRONTEND_REPO)/releases/download/$(FRONTEND_TAG)/frontend-dist.tar.gz" \
			| tar -xz -C $(STATIC_DIR); \
	fi
	@echo "Frontend installed into $(STATIC_DIR)/assets/"

# Copy from a local checkout of micro-claw-frontend (for developers).
copy-frontend:
	@echo "Copying frontend dist to $(STATIC_DIR)/..."
	rm -rf $(STATIC_DIR)/assets
	cp -r $(FRONTEND_DIR)/dist/assets $(STATIC_DIR)/assets
	@echo "Done. Frontend embedded."

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Build without frontend — TUI-only, API still works.
build:
	go build -ldflags="$(LDFLAGS)" -o bin/microagent ./cmd/microagent

# Build with frontend (downloads if not already present).
build-full: frontend build
	@echo "Built with embedded frontend."

# ---------------------------------------------------------------------------
# Test & Lint
# ---------------------------------------------------------------------------

test:
	go test -timeout 300s ./...

test-race:
	go test -race -timeout 300s ./...

integration-test: test
	go test -tags=integration ./internal/mcp/... -v -timeout 60s

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .

ci: vet lint test-race

# ---------------------------------------------------------------------------
# Misc
# ---------------------------------------------------------------------------

docker-build:
	docker build -t microagent:$(VERSION) .

clean:
	rm -rf bin/ dist/ $(STATIC_DIR)/assets/

dev-run:
	go run ./cmd/microagent $(ARGS)
