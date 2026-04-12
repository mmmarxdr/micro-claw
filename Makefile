VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

FRONTEND_DIR ?= ../micro-claw-frontend

.PHONY: build test test-race integration-test vet lint fmt ci docker-build clean dev-run copy-frontend

copy-frontend:
	@echo "Copying frontend dist to internal/web/static/..."
	rm -rf internal/web/static
	cp -r $(FRONTEND_DIR)/dist internal/web/static
	@echo "Done. Frontend embedded."

build: copy-frontend
	go build -ldflags="$(LDFLAGS)" -o bin/microagent ./cmd/microagent

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

docker-build:
	docker build -t microagent:$(VERSION) .

clean:
	rm -rf bin/ dist/

dev-run:
	go run ./cmd/microagent $(ARGS)
