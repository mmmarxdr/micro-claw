//go:build integration

package mcp

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// testServerBin holds the path to the compiled test helper binary.
// Set by TestMain before any Test* function runs.
var testServerBin string

// TestMain compiles the helper binary once into a temp directory, then runs
// the full integration test suite. The temp directory is cleaned up after
// all tests complete.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "mcp-integration-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}

	testServerBin = filepath.Join(tmpDir, "testserver")

	cmd := exec.Command("go", "build", "-o", testServerBin, "./testdata/server")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("failed to compile test server: %v", err)
	}

	defer os.RemoveAll(tmpDir)

	os.Exit(m.Run())
}

// testMCPConfig builds a config.MCPConfig that points at the test server binary.
func testMCPConfig() config.MCPConfig {
	return config.MCPConfig{
		Enabled:        true,
		ConnectTimeout: 5 * time.Second,
		Servers: []config.MCPServerConfig{
			{
				Name:      "test-server",
				Transport: "stdio",
				Command:   []string{testServerBin},
			},
		},
	}
}

// setupLiveServer calls BuildMCPTools against the live test helper binary, asserts
// success, registers t.Cleanup(manager.Close), and returns the tool map and manager.
// Used by T2 and T3 to avoid duplicating the happy-path setup.
func setupLiveServer(t *testing.T) (map[string]tool.Tool, *Manager) {
	t.Helper()

	toolMap, manager, err := BuildMCPTools(t.Context(), testMCPConfig())
	if err != nil {
		t.Fatalf("BuildMCPTools returned unexpected error: %v", err)
	}
	if toolMap == nil {
		t.Fatal("BuildMCPTools returned nil toolMap")
	}

	t.Cleanup(manager.Close)

	return toolMap, manager
}

// ---------------------------------------------------------------------------
// T1 — BuildMCPTools discovers exactly two tools from the live test server.
// ---------------------------------------------------------------------------

func TestIntegration_BuildMCPTools_Discovery(t *testing.T) {
	toolMap, manager, err := BuildMCPTools(t.Context(), testMCPConfig())
	if err != nil {
		t.Fatalf("BuildMCPTools returned unexpected error: %v", err)
	}

	t.Cleanup(manager.Close)

	if manager == nil {
		t.Errorf("manager is nil")
	}

	if len(toolMap) != 2 {
		t.Errorf("want 2 tools, got %d: %v", len(toolMap), toolMap)
	}

	if toolMap["echo_tool"] == nil {
		t.Errorf("echo_tool not found in toolMap")
	}

	if toolMap["error_tool"] == nil {
		t.Errorf("error_tool not found in toolMap")
	}
}

// ---------------------------------------------------------------------------
// T2 — Execute echo_tool returns expected text content (happy path).
// ---------------------------------------------------------------------------

func TestIntegration_Execute_EchoTool(t *testing.T) {
	toolMap, _ := setupLiveServer(t)

	echoTool, ok := toolMap["echo_tool"]
	if !ok {
		t.Fatalf("echo_tool not found in toolMap")
	}

	result, err := echoTool.Execute(t.Context(), json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if result.IsError {
		t.Errorf("want IsError=false, got true; content=%q", result.Content)
	}

	if result.Content != "hello" {
		t.Errorf("want Content=%q, got %q", "hello", result.Content)
	}
}

// ---------------------------------------------------------------------------
// T3 — Execute error_tool returns MCP-level error (IsError: true, no Go error).
// ---------------------------------------------------------------------------

func TestIntegration_Execute_ErrorTool(t *testing.T) {
	toolMap, _ := setupLiveServer(t)

	errorTool, ok := toolMap["error_tool"]
	if !ok {
		t.Fatalf("error_tool not found in toolMap")
	}

	result, err := errorTool.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected Go error: %v", err)
	}

	if !result.IsError {
		t.Errorf("want IsError=true, got false; content=%q", result.Content)
	}

	if result.Content == "" {
		t.Errorf("want non-empty Content for error_tool, got empty string")
	}
}

// ---------------------------------------------------------------------------
// T4 — Pre-cancelled context returns empty tool map without panic.
// ---------------------------------------------------------------------------

func TestIntegration_CancelledContext_ReturnsEmptyMap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before calling BuildMCPTools

	toolMap, manager, err := BuildMCPTools(ctx, testMCPConfig())

	t.Cleanup(manager.Close) // manager is always non-nil, even when empty

	if err != nil {
		t.Errorf("BuildMCPTools should not return a Go error on server connect failure, got: %v", err)
	}

	if len(toolMap) != 0 {
		t.Errorf("want empty toolMap with cancelled ctx, got %d tools", len(toolMap))
	}
}

// ---------------------------------------------------------------------------
// T5 — Manager.Close terminates the subprocess (Linux only).
// ---------------------------------------------------------------------------

// pidFromManager extracts the subprocess PID by walking through the manager's
// server list, casting the client to *client.Client, getting the transport, and
// using reflect + unsafe to read the unexported cmd *exec.Cmd field from
// *transport.Stdio.
//
// This is a test-only helper. Using unsafe to read unexported struct fields is
// acceptable here because:
//  1. There is no public GetCmd()/GetPID() API in mcp-go v0.45.0.
//  2. This code only runs in the integration test binary, never in production.
//  3. If mcp-go adds a public accessor in a future version, replace this.
//  4. reflect.Value.Interface() panics on unexported fields in Go 1.17+; unsafe
//     is the only reliable way to read them without modifying the library.
func pidFromManager(t *testing.T, mgr *Manager) int {
	t.Helper()

	if len(mgr.servers) == 0 {
		t.Fatal("manager has no servers")
	}

	// managedServer.client is MCPCaller; in production it is *client.Client.
	c, ok := mgr.servers[0].client.(*client.Client)
	if !ok {
		t.Fatal("unexpected client type (not *client.Client)")
	}

	// client.GetTransport() returns transport.Interface; the concrete type is *transport.Stdio.
	stdio, ok := c.GetTransport().(*transport.Stdio)
	if !ok {
		t.Fatal("unexpected transport type (not *transport.Stdio)")
	}

	// transport.Stdio.cmd is unexported; use reflect.Type to locate its byte offset,
	// then unsafe.Pointer to read the *exec.Cmd value without triggering the
	// reflect.Value.Interface() panic that fires on unexported fields in Go 1.17+.
	// Use reflect.TypeOf(stdio).Elem() (pointer→element) to avoid copying the
	// mutex-containing struct, which would trigger go vet's copylock check.
	stdioType := reflect.TypeOf(stdio).Elem() // *Stdio → Stdio without copying
	var cmdOffset uintptr
	var found bool
	for i := range stdioType.NumField() {
		if stdioType.Field(i).Name == "cmd" {
			cmdOffset = stdioType.Field(i).Offset
			found = true
			break
		}
	}
	if !found {
		t.Fatal("transport.Stdio.cmd field offset not found via reflect.Type")
	}

	//nolint:gosec // unsafe is intentional — test-only PID extraction
	cmd := *(**exec.Cmd)(unsafe.Pointer(uintptr(unsafe.Pointer(stdio)) + cmdOffset))
	if cmd == nil {
		t.Fatal("transport.Stdio.cmd is nil")
	}
	if cmd.Process == nil {
		t.Fatal("transport.Stdio.cmd.Process is nil")
	}

	return cmd.Process.Pid
}

func TestIntegration_Close_TerminatesSubprocess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("subprocess lifecycle test is Linux-only")
	}

	toolMap, manager, err := BuildMCPTools(t.Context(), testMCPConfig())
	if err != nil {
		t.Fatalf("BuildMCPTools returned unexpected error: %v", err)
	}

	if len(toolMap) != 2 {
		t.Fatalf("want 2 tools, got %d", len(toolMap))
	}

	pid := pidFromManager(t, manager)

	// Assert subprocess is alive before Close.
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("os.FindProcess(%d) failed: %v", pid, err)
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("subprocess PID %d not alive before Close: %v", pid, err)
	}

	manager.Close()

	// Poll for process exit for up to 2 seconds.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break // process gone — assertion will pass below
		}

		time.Sleep(100 * time.Millisecond)
	}

	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("subprocess PID %d still alive after manager.Close()", pid)
	}
}
