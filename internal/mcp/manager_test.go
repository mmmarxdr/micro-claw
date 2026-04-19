package mcp

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
)

// ---------------------------------------------------------------------------
// TestEnvForServer
// ---------------------------------------------------------------------------

// TestEnvForServer_Empty — nil Env → nil slice returned.
func TestEnvForServer_Empty(t *testing.T) {
	cfg := config.MCPServerConfig{Name: "srv", Env: nil}
	got := envForServer(cfg)
	if got != nil {
		t.Errorf("expected nil slice for empty Env, got %v", got)
	}
}

// TestEnvForServer_Literal — env values without ${VAR} refs pass through as-is.
func TestEnvForServer_Literal(t *testing.T) {
	cfg := config.MCPServerConfig{
		Name: "srv",
		Env:  map[string]string{"TOKEN": "abc123", "DEBUG": "1"},
	}
	got := envForServer(cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	sort.Strings(got)
	// After sorting: DEBUG=1, TOKEN=abc123
	if !strings.Contains(got[0], "DEBUG=1") {
		t.Errorf("expected DEBUG=1 in %v", got)
	}
	if !strings.Contains(got[1], "TOKEN=abc123") {
		t.Errorf("expected TOKEN=abc123 in %v", got)
	}
}

// TestEnvForServer_ExpandsVars — ${VAR} in value is expanded using os env.
func TestEnvForServer_ExpandsVars(t *testing.T) {
	t.Setenv("MY_SECRET", "hunter2")

	cfg := config.MCPServerConfig{
		Name: "srv",
		Env:  map[string]string{"PASS": "${MY_SECRET}"},
	}
	got := envForServer(cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(got), got)
	}
	if got[0] != "PASS=hunter2" {
		t.Errorf("expected PASS=hunter2, got %q", got[0])
	}
}

// TestEnvForServer_MissingVar — unset ${VAR} falls back to raw value (fail-soft).
func TestEnvForServer_MissingVar(t *testing.T) {
	// Ensure the env var is not set in this test.
	os.Unsetenv("DEFINITELY_NOT_SET_XYZ123") //nolint:errcheck

	cfg := config.MCPServerConfig{
		Name: "srv",
		Env:  map[string]string{"KEY": "${DEFINITELY_NOT_SET_XYZ123}"},
	}
	got := envForServer(cfg)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d: %v", len(got), got)
	}
	// Fail-soft: raw unexpanded value is used.
	if got[0] != "KEY=${DEFINITELY_NOT_SET_XYZ123}" {
		t.Errorf("expected raw fallback, got %q", got[0])
	}
}

// ---------------------------------------------------------------------------
// TestConnectStdio_NonexistentCommand
// ---------------------------------------------------------------------------

// TestConnectStdio_NonexistentCommand exercises the error path of connectStdio
// with a command that cannot be found, covering the connect attempt code path.
func TestConnectStdio_NonexistentCommand(t *testing.T) {
	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
		Command:   []string{"/nonexistent/binary/that/does/not/exist"},
	}
	_, err := connectStdio(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for nonexistent command, got nil")
	}
}

// TestConnectHTTP_BadURL exercises the error path of connectHTTP with a URL
// that will fail to connect, covering the HTTP connect attempt code path.
func TestConnectHTTP_BadURL(t *testing.T) {
	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "http",
		URL:       "http://127.0.0.1:1", // port 1 is reserved; connection will be refused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := connectHTTP(ctx, cfg)
	if err == nil {
		t.Error("expected error for bad URL, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestManager_Close_Empty
// ---------------------------------------------------------------------------

// TestManager_Close_Empty verifies that calling Close on an empty Manager
// does not panic and returns cleanly.
func TestManager_Close_Empty(t *testing.T) {
	m := &Manager{}
	m.Close() // must not panic
}

// ---------------------------------------------------------------------------
// TestManager_Close_AllClosed
// ---------------------------------------------------------------------------

// TestManager_Close_AllClosed verifies that Close calls Close() on every
// managed server exactly once when all Close() calls return nil.
func TestManager_Close_AllClosed(t *testing.T) {
	closeCalls := make([]int, 3)

	servers := make([]managedServer, 3)
	for i := range servers {
		idx := i // capture
		servers[i] = managedServer{
			cfg: config.MCPServerConfig{Name: "srv"},
			client: &mockMCPCaller{
				closeFn: func() error {
					closeCalls[idx]++
					return nil
				},
			},
		}
	}

	m := &Manager{servers: servers}
	m.Close()

	for i, count := range closeCalls {
		if count != 1 {
			t.Errorf("server[%d].Close() called %d times, expected exactly 1", i, count)
		}
	}
}

// ---------------------------------------------------------------------------
// TestManager_Close_Error
// ---------------------------------------------------------------------------

// TestManager_Close_Error verifies that Close continues calling Close() on
// remaining servers even when one returns an error (no short-circuit).
func TestManager_Close_Error(t *testing.T) {
	closeErr := errors.New("connection reset")
	closeCalls := make([]int, 3)

	servers := make([]managedServer, 3)
	for i := range servers {
		idx := i // capture
		var returnErr error
		if i == 1 {
			returnErr = closeErr // middle server fails
		}
		servers[i] = managedServer{
			cfg: config.MCPServerConfig{Name: "srv"},
			client: &mockMCPCaller{
				closeFn: func() error {
					closeCalls[idx]++
					return returnErr
				},
			},
		}
	}

	m := &Manager{servers: servers}
	m.Close() // must not panic, must not short-circuit

	for i, count := range closeCalls {
		if count != 1 {
			t.Errorf("server[%d].Close() called %d times, expected exactly 1 (no short-circuit)", i, count)
		}
	}
}
