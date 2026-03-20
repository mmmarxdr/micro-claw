package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"microagent/internal/config"
)

// writeTestConfig writes a minimal valid YAML config to dir/config.yaml and returns its path.
func writeTestConfig(t *testing.T, dir string, servers []config.MCPServerConfig, enabled bool) string {
	t.Helper()
	cfg := config.Config{}
	cfg.Tools.MCP.Enabled = enabled
	cfg.Tools.MCP.Servers = servers
	// Provide minimum required fields so the file is non-empty YAML.
	cfg.Provider.APIKey = "test-key"
	cfg.Provider.Type = "anthropic"
	cfg.Provider.Model = "claude-3-sonnet-20240229"

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func stdioServer(name string) config.MCPServerConfig {
	return config.MCPServerConfig{
		Name:      name,
		Transport: "stdio",
		Command:   []string{"true"},
	}
}

func httpServer(name string) config.MCPServerConfig {
	return config.MCPServerConfig{
		Name:      name,
		Transport: "http",
		URL:       "http://localhost:9999/sse",
	}
}

func TestMCPService_List_Empty(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{}, true)
	svc := NewMCPService(path)
	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statuses == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(statuses) != 0 {
		t.Fatalf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestMCPService_List_MCPDisabled(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("s1")}, false)
	svc := NewMCPService(path)
	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("expected empty slice for disabled MCP, got %d entries", len(statuses))
	}
}

func TestMCPService_List_WithServers(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{
		stdioServer("alpha"),
		httpServer("beta"),
	}, true)
	svc := NewMCPService(path)
	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	if statuses[0].Config.Name != "alpha" {
		t.Errorf("expected first server 'alpha', got %q", statuses[0].Config.Name)
	}
	if statuses[1].Config.Name != "beta" {
		t.Errorf("expected second server 'beta', got %q", statuses[1].Config.Name)
	}
}

func TestMCPService_Add_Success(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("existing")}, true)
	svc := NewMCPService(path)

	err := svc.Add(context.Background(), stdioServer("newserver"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 servers after add, got %d", len(statuses))
	}
	if statuses[1].Config.Name != "newserver" {
		t.Errorf("expected new server at index 1, got %q", statuses[1].Config.Name)
	}
}

func TestMCPService_Add_Duplicate(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("myserver")}, true)
	svc := NewMCPService(path)

	// Read the original file content.
	origContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	err = svc.Add(context.Background(), stdioServer("myserver"))
	if err == nil {
		t.Fatal("expected error for duplicate, got nil")
	}
	if !errors.Is(err, ErrDuplicateName) {
		t.Errorf("expected ErrDuplicateName, got %v", err)
	}

	// Confirm file unchanged.
	newContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after failed add: %v", err)
	}
	if string(origContent) != string(newContent) {
		t.Error("config file was modified after duplicate add error")
	}
}

func TestMCPService_Add_ValidationFailure(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("existing")}, true)
	svc := NewMCPService(path)

	origContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// Empty name should fail validation.
	bad := config.MCPServerConfig{Name: "", Transport: "stdio", Command: []string{"true"}}
	err = svc.Add(context.Background(), bad)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}

	newContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after failed add: %v", err)
	}
	if string(origContent) != string(newContent) {
		t.Error("config file was modified after validation failure")
	}
}

func TestMCPService_Remove_Success(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{
		stdioServer("alpha"),
		httpServer("beta"),
	}, true)
	svc := NewMCPService(path)

	err := svc.Remove(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 server after remove, got %d", len(statuses))
	}
	if statuses[0].Config.Name != "beta" {
		t.Errorf("expected remaining server 'beta', got %q", statuses[0].Config.Name)
	}
}

func TestMCPService_Remove_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("existing")}, true)
	svc := NewMCPService(path)

	origContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	err = svc.Remove(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	newContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after failed remove: %v", err)
	}
	if string(origContent) != string(newContent) {
		t.Error("config file was modified after not-found remove error")
	}
}

func TestMCPService_Remove_Last(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("only")}, true)
	svc := NewMCPService(path)

	err := svc.Remove(context.Background(), "only")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	statuses, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("expected empty slice after removing last server, got %d", len(statuses))
	}
}

func TestMCPService_Validate_Valid_Stdio(t *testing.T) {
	svc := &MCPService{}
	err := svc.Validate(stdioServer("valid-name"))
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestMCPService_Validate_Valid_HTTP(t *testing.T) {
	svc := &MCPService{}
	err := svc.Validate(httpServer("my-http-server"))
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestMCPService_Validate_EmptyName(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "", Transport: "stdio", Command: []string{"cmd"}}
	err := svc.Validate(cfg)
	if err == nil {
		t.Error("expected error for empty name, got nil")
	}
}

func TestMCPService_Validate_InvalidNameChars(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "my server!", Transport: "stdio", Command: []string{"cmd"}}
	err := svc.Validate(cfg)
	if err == nil {
		t.Error("expected error for name with invalid chars, got nil")
	}
}

func TestMCPService_Validate_InvalidTransport(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "valid", Transport: "grpc"}
	err := svc.Validate(cfg)
	if err == nil {
		t.Error("expected error for unknown transport, got nil")
	}
}

func TestMCPService_Validate_MissingCommand(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "valid", Transport: "stdio", Command: []string{}}
	err := svc.Validate(cfg)
	if err == nil {
		t.Error("expected error for missing command, got nil")
	}
}

func TestMCPService_Validate_MissingURL(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "valid", Transport: "http", URL: ""}
	err := svc.Validate(cfg)
	if err == nil {
		t.Error("expected error for missing URL, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestMCPService_Test_* — tests for testWithConnector (injectable connector)
// ---------------------------------------------------------------------------

func TestMCPService_Test_Success(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("s1")}, true)
	svc := NewMCPService(path)

	mock := &mockListableClient{toolNames: []string{"tool_a", "tool_b", "tool_c"}}
	connector := func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return mock, nil
	}

	cfg := config.MCPServerConfig{Name: "s1", Transport: "stdio", Command: []string{"echo"}}
	names, err := svc.testWithConnector(context.Background(), cfg, connector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 tool names, got %d: %v", len(names), names)
	}
	for _, want := range []string{"tool_a", "tool_b", "tool_c"} {
		found := false
		for _, got := range names {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected tool %q in names, got: %v", want, names)
		}
	}
}

func TestMCPService_Test_PrefixTools(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, nil, true)
	svc := NewMCPService(path)

	mock := &mockListableClient{toolNames: []string{"create_issue"}}
	connector := func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return mock, nil
	}

	cfg := config.MCPServerConfig{Name: "gh", Transport: "stdio", Command: []string{"gh-mcp"}, PrefixTools: true}
	names, err := svc.testWithConnector(context.Background(), cfg, connector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 tool name, got %d: %v", len(names), names)
	}
	if names[0] != "gh_create_issue" {
		t.Errorf("expected %q, got %q", "gh_create_issue", names[0])
	}
}

func TestMCPService_Test_ConnectError(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, nil, true)
	svc := NewMCPService(path)

	wantErr := errors.New("connect failed")
	connector := func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return nil, wantErr
	}

	cfg := config.MCPServerConfig{Name: "srv", Transport: "stdio", Command: []string{"echo"}}
	_, err := svc.testWithConnector(context.Background(), cfg, connector)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped wantErr, got: %v", err)
	}
}

func TestMCPService_Test_UnknownTransport(t *testing.T) {
	svc := &MCPService{}
	cfg := config.MCPServerConfig{Name: "srv", Transport: "grpc"}
	_, err := svc.Test(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unknown transport, got nil")
	}
}

func TestMCPService_Test_CloseCalledOnError(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, nil, true)
	svc := NewMCPService(path)

	mock := &mockListableClient{
		listErr: errors.New("list tools failed"),
	}
	connector := func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return mock, nil
	}

	cfg := config.MCPServerConfig{Name: "srv", Transport: "stdio", Command: []string{"echo"}}
	_, err := svc.testWithConnector(context.Background(), cfg, connector)
	if err == nil {
		t.Fatal("expected error from ListTools, got nil")
	}
	if mock.closeCalls != 1 {
		t.Errorf("expected Close() called once, got %d calls", mock.closeCalls)
	}
}

func TestMCPService_AtomicWrite_NoOrphan(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, []config.MCPServerConfig{stdioServer("existing")}, true)
	svc := NewMCPService(path)

	err := svc.Add(context.Background(), stdioServer("newone"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Confirm no .tmp file left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("orphan .tmp file found: %s", e.Name())
		}
	}
}
