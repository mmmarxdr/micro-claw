package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"daimon/internal/mcp"
)

// writeTempConfig writes a minimal valid YAML config file to a temp directory
// and returns its path. The YAML uses the inline block format for compactness.
func writeTempConfig(t *testing.T, yamlContent string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// minimalConfig returns a minimal valid config YAML with MCP enabled and no servers.
const minimalConfig = `
provider:
  type: anthropic
  model: claude-3-sonnet-20240229
  api_key: test-key
tools:
  mcp:
    enabled: true
    servers: []
`

// minimalConfigWithServer returns a minimal config with one stdio server.
const minimalConfigWithServer = `
provider:
  type: anthropic
  model: claude-3-sonnet-20240229
  api_key: test-key
tools:
  mcp:
    enabled: true
    servers:
      - name: myserver
        transport: stdio
        command: [echo, hello]
`

// minimalConfigDisabled returns a minimal config with MCP disabled.
const minimalConfigDisabled = `
provider:
  type: anthropic
  model: claude-3-sonnet-20240229
  api_key: test-key
tools:
  mcp:
    enabled: false
    servers: []
`

// ---------------------------------------------------------------------------
// mcpList tests
// ---------------------------------------------------------------------------

func TestMCPList_Empty(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	err := mcpList([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPList_WithServer(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	err := mcpList([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMCPList_JSONFlag(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	err := mcpList([]string{"--json"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error with --json: %v", err)
	}
}

func TestMCPList_NoConfig(t *testing.T) {
	err := mcpList([]string{}, "/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent config, got nil")
	}
}

// ---------------------------------------------------------------------------
// mcpAdd tests
// ---------------------------------------------------------------------------

func TestMCPAdd_Success(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	err := mcpAdd([]string{
		"--name", "test",
		"--transport", "stdio",
		"--command", "echo hello",
		"--no-test",
	}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify server was added.
	svc := mcp.NewMCPService(cfgPath)
	servers, listErr := svc.List(context.Background())
	if listErr != nil {
		t.Fatalf("list after add: %v", listErr)
	}
	if len(servers) != 1 || servers[0].Config.Name != "test" {
		t.Fatalf("expected server 'test', got %v", servers)
	}
}

func TestMCPAdd_Duplicate(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	err := mcpAdd([]string{
		"--name", "myserver",
		"--transport", "stdio",
		"--command", "echo hello",
		"--no-test",
	}, cfgPath)
	if err == nil {
		t.Fatal("expected error for duplicate server, got nil")
	}
	if !errors.Is(err, mcp.ErrDuplicateName) {
		t.Errorf("expected ErrDuplicateName, got: %v", err)
	}
}

func TestMCPAdd_ValidationError(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	// Missing --name should fail validation (empty name).
	err := mcpAdd([]string{
		"--transport", "stdio",
		"--command", "echo hello",
		"--no-test",
	}, cfgPath)
	if err == nil {
		t.Fatal("expected validation error for empty name, got nil")
	}
}

func TestMCPAdd_NoConfig(t *testing.T) {
	err := mcpAdd([]string{
		"--name", "test",
		"--transport", "stdio",
		"--command", "echo",
		"--no-test",
	}, "/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent config, got nil")
	}
}

// ---------------------------------------------------------------------------
// mcpRemove tests
// ---------------------------------------------------------------------------

func TestMCPRemove_Success(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	err := mcpRemove([]string{"--yes", "myserver"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	svc := mcp.NewMCPService(cfgPath)
	servers, _ := svc.List(context.Background())
	if len(servers) != 0 {
		t.Fatalf("expected 0 servers after remove, got %d", len(servers))
	}
}

func TestMCPRemove_NotFound(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	err := mcpRemove([]string{"--yes", "nonexistent"}, cfgPath)
	if err == nil {
		t.Fatal("expected error for not-found server, got nil")
	}
	if !errors.Is(err, mcp.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestMCPRemove_MissingName(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	err := mcpRemove([]string{"--yes"}, cfgPath)
	if err == nil {
		t.Fatal("expected error when name is missing, got nil")
	}
}

func TestMCPRemove_NoYesNonTTY(t *testing.T) {
	// When stdin is not a TTY and --yes not provided, expect error.
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	// stdin in test context is not a TTY, so omitting --yes should error.
	err := mcpRemove([]string{"myserver"}, cfgPath)
	if err == nil {
		t.Fatal("expected error when --yes not provided on non-TTY stdin, got nil")
	}
}

// ---------------------------------------------------------------------------
// mcpValidate tests
// ---------------------------------------------------------------------------

func TestMCPValidate_Valid(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigWithServer)
	err := mcpValidate([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error for valid config: %v", err)
	}
}

func TestMCPValidate_Disabled(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfigDisabled)
	err := mcpValidate([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error for disabled MCP: %v", err)
	}
}

func TestMCPValidate_Empty(t *testing.T) {
	cfgPath := writeTempConfig(t, minimalConfig)
	err := mcpValidate([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error for empty servers: %v", err)
	}
}

func TestMCPValidate_InvalidServer(t *testing.T) {
	// A server with an invalid name (spaces) should fail validation.
	cfgPath := writeTempConfig(t, `
provider:
  type: anthropic
  model: claude-3-sonnet-20240229
  api_key: test-key
tools:
  mcp:
    enabled: true
    servers:
      - name: "bad name!"
        transport: stdio
        command: [echo]
`)
	err := mcpValidate([]string{}, cfgPath)
	if err == nil {
		t.Fatal("expected validation error for invalid server name, got nil")
	}
}

func TestMCPValidate_NoConfig(t *testing.T) {
	err := mcpValidate([]string{}, "/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent config, got nil")
	}
}
