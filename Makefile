.PHONY: test integration-test build lint fmt dev-run

test:
	go test ./...

integration-test: test
	go test -tags=integration ./internal/mcp/... -v -timeout 60s

build:
	go build -ldflags="-s -w" -o microagent ./cmd/microagent

lint:
	golangci-lint run

fmt:
	gofumpt -w .

dev-run:
	./dev.sh run $(ARGS)
