package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"daimon/internal/config"
	"daimon/internal/tool"
)

// ---------------------------------------------------------------------------
// helpers to build fake serverResults for collision / prefix tests
// ---------------------------------------------------------------------------

// buildToolMap simulates the post-fan-out assembly logic in BuildMCPTools.
// It applies the same first-writer-wins deduplication policy.
func buildToolMapFromResults(results []serverResult) map[string]tool.Tool {
	toolMap := make(map[string]tool.Tool)
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for _, t := range r.tools {
			if _, exists := toolMap[t.Name()]; exists {
				continue // first wins
			}
			toolMap[t.Name()] = t
		}
	}
	return toolMap
}

// makeTool creates a minimal MCPToolAdapter with the given name.
func makeTool(caller MCPCaller, name string) tool.Tool {
	return &MCPToolAdapter{
		caller:  caller,
		toolDef: mcp.Tool{Name: name},
	}
}

// ---------------------------------------------------------------------------
// mockListableClient — satisfies listableClient (MCPCaller + ListTools)
// ---------------------------------------------------------------------------

// mockListableClient is a test double that implements listableClient.
// toolNames is the list of tool names returned by ListTools.
// closeErr is returned by Close(); nil means success.
// listErr is returned by ListTools(); nil means success.
type mockListableClient struct {
	toolNames []string
	closeErr  error
	listErr   error
	closeCalls int
}

func (m *mockListableClient) CallTool(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func (m *mockListableClient) Close() error {
	m.closeCalls++
	return m.closeErr
}

func (m *mockListableClient) ListTools(_ context.Context, _ mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	tools := make([]mcp.Tool, 0, len(m.toolNames))
	for _, name := range m.toolNames {
		tools = append(tools, mcp.Tool{Name: name})
	}
	return &mcp.ListToolsResult{Tools: tools}, nil
}

// mockConnectorOf returns a ConnectorFunc that always yields the given client.
func mockConnectorOf(lc listableClient) ConnectorFunc {
	return func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return lc, nil
	}
}

// mockConnectorCapture returns a ConnectorFunc that records the last cfg it
// received and always yields the given client.
func mockConnectorCapture(lc listableClient, captured *config.MCPServerConfig) ConnectorFunc {
	return func(_ context.Context, cfg config.MCPServerConfig) (listableClient, error) {
		*captured = cfg
		return lc, nil
	}
}

// errorConnector returns a ConnectorFunc that always returns the given error.
//
//nolint:unused // kept for future error-path tests
func errorConnector(err error) ConnectorFunc {
	return func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		return nil, err
	}
}

// callCountingConnector wraps a ConnectorFunc and increments a counter each call.
type callCountingConnector struct {
	fn    ConnectorFunc
	calls int
}

func (c *callCountingConnector) connect(ctx context.Context, cfg config.MCPServerConfig) (listableClient, error) {
	c.calls++
	return c.fn(ctx, cfg)
}

// neverCalledConnector panics if invoked — useful to assert a connector is not called.
func neverCalledConnector(t *testing.T, label string) ConnectorFunc {
	t.Helper()
	return func(_ context.Context, _ config.MCPServerConfig) (listableClient, error) {
		t.Errorf("connector %q should not have been called", label)
		return nil, errors.New("should not be called")
	}
}

// ---------------------------------------------------------------------------
// TestBuildMCPTools_Disabled
// ---------------------------------------------------------------------------

func TestBuildMCPTools_Disabled(t *testing.T) {
	t.Run("mcp disabled returns empty map nil manager", func(t *testing.T) {
		cfg := config.MCPConfig{Enabled: false}
		toolMap, manager, err := BuildMCPTools(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(toolMap) != 0 {
			t.Errorf("expected empty tool map, got %d entries", len(toolMap))
		}
		if manager == nil {
			t.Error("expected non-nil manager (empty)")
		}
	})

	t.Run("no servers returns empty map", func(t *testing.T) {
		cfg := config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{}}
		toolMap, manager, err := BuildMCPTools(context.Background(), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(toolMap) != 0 {
			t.Errorf("expected empty tool map, got %d entries", len(toolMap))
		}
		if manager == nil {
			t.Error("expected non-nil manager (empty)")
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildToolMap_CollisionAndPrefix (tests the assembly logic in isolation)
// ---------------------------------------------------------------------------

func TestBuildToolMap_InterMCPCollision(t *testing.T) {
	mock := &mockMCPCaller{
		callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}

	// Two server results both expose "search"
	results := []serverResult{
		{
			cfg: config.MCPServerConfig{Name: "serverA"},
			tools: []tool.Tool{
				makeTool(mock, "search"),
				makeTool(mock, "list"),
			},
		},
		{
			cfg: config.MCPServerConfig{Name: "serverB"},
			tools: []tool.Tool{
				makeTool(mock, "search"), // collision with serverA
				makeTool(mock, "other"),
			},
		},
	}

	toolMap := buildToolMapFromResults(results)

	if _, ok := toolMap["search"]; !ok {
		t.Error("expected 'search' to be in the tool map")
	}
	if len(toolMap) != 3 { // search, list, other
		t.Errorf("expected 3 tools, got %d: %v", len(toolMap), toolMap)
	}
	// Verify 'other' and 'list' are present
	for _, name := range []string{"list", "other"} {
		if _, ok := toolMap[name]; !ok {
			t.Errorf("expected %q in tool map", name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestPrefixTools
// ---------------------------------------------------------------------------

func TestPrefixTools(t *testing.T) {
	t.Run("prefix_tools applies server name prefix", func(t *testing.T) {
		mock := &mockMCPCaller{
			callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		}

		// Simulate what BuildMCPTools does when PrefixTools is true:
		// the adapter's toolDef.Name is set to "<server>_<tool>" before wrapping.
		serverName := "myfs"
		originalToolName := "read_file"
		prefixedName := serverName + "_" + originalToolName

		results := []serverResult{
			{
				cfg: config.MCPServerConfig{Name: serverName, PrefixTools: true},
				tools: []tool.Tool{
					&MCPToolAdapter{
						caller:  mock,
						toolDef: mcp.Tool{Name: prefixedName},
					},
				},
			},
		}

		toolMap := buildToolMapFromResults(results)

		if _, ok := toolMap[prefixedName]; !ok {
			t.Errorf("expected %q in tool map, got keys: %v", prefixedName, toolMap)
		}
		if _, ok := toolMap[originalToolName]; ok {
			t.Errorf("unexpected raw tool name %q in tool map", originalToolName)
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildToolMap_FailedServerOtherSucceeds
// ---------------------------------------------------------------------------

func TestBuildToolMap_FailedServerOtherSucceeds(t *testing.T) {
	mock := &mockMCPCaller{
		callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}

	results := []serverResult{
		{
			cfg: config.MCPServerConfig{Name: "serverA"},
			err: errFakeConnectFailure, //nolint:goerr113
		},
		{
			cfg: config.MCPServerConfig{Name: "serverB"},
			tools: []tool.Tool{
				makeTool(mock, "b_tool"),
			},
		},
	}

	toolMap := buildToolMapFromResults(results)

	if _, ok := toolMap["b_tool"]; !ok {
		t.Error("expected 'b_tool' from successful server, not found")
	}
	if len(toolMap) != 1 {
		t.Errorf("expected 1 tool, got %d", len(toolMap))
	}
}

// errFakeConnectFailure simulates a server connection failure in tests.
type fakeConnectError struct{ msg string }

func (e *fakeConnectError) Error() string { return e.msg }

var errFakeConnectFailure = &fakeConnectError{msg: "simulated connect failure"}

// ---------------------------------------------------------------------------
// TestConnectStdioListable / TestConnectHTTPListable — exercise error paths
// ---------------------------------------------------------------------------

func TestConnectStdioListable_BadCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
		Command:   []string{"definitely-not-a-real-command-xyzzy123"},
	}
	_, err := connectStdioListable(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
}

func TestConnectHTTPListable_BadURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "http",
		URL:       "http://localhost:1", // port 1 is reserved/unreachable
	}
	_, err := connectHTTPListable(ctx, cfg)
	if err == nil {
		t.Fatal("expected error for unreachable URL, got nil")
	}
}

func TestBuildMCPTools_EmptyManagerAfterAllFail(_ *testing.T) {
	// This test documents the behavior: if a server fails to connect,
	// its client is not added to the Manager. The Manager.Close() must
	// still be safe to call (no-op on empty servers).
	manager := &Manager{}
	manager.Close() // must not panic
}

// ---------------------------------------------------------------------------
// NEW: BuildMCPToolsWithConnector tests (Tasks 1.6–1.8)
// ---------------------------------------------------------------------------

// TestBuildMCPTools_WithConnector_Disabled — Enabled=false → nil map, non-nil Manager, nil error.
func TestBuildMCPTools_WithConnector_Disabled(t *testing.T) {
	cfg := config.MCPConfig{Enabled: false}
	noConnector := neverCalledConnector(t, "stdio")
	toolMap, mgr, err := BuildMCPToolsWithConnector(context.Background(), cfg, noConnector, noConnector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toolMap != nil {
		t.Errorf("expected nil map when disabled, got len=%d", len(toolMap))
	}
	if mgr == nil {
		t.Error("expected non-nil Manager")
	}
}

// TestBuildMCPTools_WithConnector_EmptyServers — Enabled, no servers → empty (non-nil) map, nil error.
func TestBuildMCPTools_WithConnector_EmptyServers(t *testing.T) {
	cfg := config.MCPConfig{Enabled: true, Servers: nil}
	noConnector := neverCalledConnector(t, "stdio")
	toolMap, mgr, err := BuildMCPToolsWithConnector(context.Background(), cfg, noConnector, noConnector)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toolMap == nil {
		t.Error("expected non-nil empty map")
	}
	if len(toolMap) != 0 {
		t.Errorf("expected 0 tools, got %d", len(toolMap))
	}
	if mgr == nil {
		t.Error("expected non-nil Manager")
	}
}

// TestBuildMCPTools_WithConnector_StdioSuccess — 1 server, mock returns 3 tools → map has 3 entries.
func TestBuildMCPTools_WithConnector_StdioSuccess(t *testing.T) {
	mock := &mockListableClient{toolNames: []string{"tool_a", "tool_b", "tool_c"}}
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "test-server", Transport: "stdio", Command: []string{"echo"}},
		},
	}
	toolMap, mgr, err := BuildMCPToolsWithConnector(
		context.Background(), cfg,
		mockConnectorOf(mock),
		neverCalledConnector(t, "http"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolMap) != 3 {
		t.Errorf("expected 3 tools, got %d: %v", len(toolMap), toolMap)
	}
	for _, name := range []string{"tool_a", "tool_b", "tool_c"} {
		if _, ok := toolMap[name]; !ok {
			t.Errorf("expected tool %q in map", name)
		}
	}
	if len(mgr.servers) != 1 {
		t.Errorf("expected 1 server in manager, got %d", len(mgr.servers))
	}
}

// TestBuildMCPTools_WithConnector_PartialFailure — 2 servers: mock1 ok (2 tools), mock2 error → map has 2 entries, outer error nil.
func TestBuildMCPTools_WithConnector_PartialFailure(t *testing.T) {
	mock1 := &mockListableClient{toolNames: []string{"tool_a", "tool_b"}}
	failErr := errors.New("server 2 connection failed")

	srvs := []config.MCPServerConfig{
		{Name: "ok-server", Transport: "stdio", Command: []string{"echo"}},
		{Name: "bad-server", Transport: "stdio", Command: []string{"bad"}},
	}
	cfg := config.MCPConfig{Enabled: true, Servers: srvs}

	// Use a connector that succeeds for ok-server and fails for bad-server.
	stdioConn := func(_ context.Context, srv config.MCPServerConfig) (listableClient, error) {
		if srv.Name == "ok-server" {
			return mock1, nil
		}
		return nil, failErr
	}

	toolMap, mgr, err := BuildMCPToolsWithConnector(
		context.Background(), cfg,
		stdioConn,
		neverCalledConnector(t, "http"),
	)
	if err != nil {
		t.Fatalf("outer error must be nil, got: %v", err)
	}
	if len(toolMap) != 2 {
		t.Errorf("expected 2 tools from ok-server, got %d", len(toolMap))
	}
	if len(mgr.servers) != 1 {
		t.Errorf("expected 1 server in manager (only ok-server), got %d", len(mgr.servers))
	}
}

// TestBuildMCPTools_WithConnector_NameCollision — 2 servers same tool name → map has 1 entry, no outer error.
func TestBuildMCPTools_WithConnector_NameCollision(t *testing.T) {
	mock1 := &mockListableClient{toolNames: []string{"shared_tool", "unique_a"}}
	mock2 := &mockListableClient{toolNames: []string{"shared_tool", "unique_b"}}

	srvs := []config.MCPServerConfig{
		{Name: "server1", Transport: "stdio", Command: []string{"echo"}},
		{Name: "server2", Transport: "http", URL: "http://example.com"},
	}
	cfg := config.MCPConfig{Enabled: true, Servers: srvs}

	stdioConn := mockConnectorOf(mock1)
	httpConn := mockConnectorOf(mock2)

	toolMap, _, err := BuildMCPToolsWithConnector(context.Background(), cfg, stdioConn, httpConn)
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	// shared_tool appears in both; first-writer-wins → 1 copy
	// unique_a and unique_b each appear once → total 3
	if len(toolMap) != 3 {
		t.Errorf("expected 3 tools (shared_tool once + unique_a + unique_b), got %d: %v", len(toolMap), toolMap)
	}
	if _, ok := toolMap["shared_tool"]; !ok {
		t.Error("expected shared_tool in map")
	}
}

// TestBuildMCPTools_WithConnector_PrefixTools — PrefixTools=true, server "gh", tool "create_issue" → key is "gh_create_issue".
func TestBuildMCPTools_WithConnector_PrefixTools(t *testing.T) {
	mock := &mockListableClient{toolNames: []string{"create_issue"}}
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "gh", Transport: "stdio", Command: []string{"echo"}, PrefixTools: true},
		},
	}

	toolMap, _, err := BuildMCPToolsWithConnector(
		context.Background(), cfg,
		mockConnectorOf(mock),
		neverCalledConnector(t, "http"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := toolMap["gh_create_issue"]; !ok {
		t.Errorf("expected key 'gh_create_issue' in map, got keys: %v", toolMap)
	}
	if _, ok := toolMap["create_issue"]; ok {
		t.Error("raw tool name 'create_issue' should not be in map when PrefixTools=true")
	}
}

// TestBuildMCPTools_WithConnector_HTTPTransport — HTTP server → httpConnector called, not stdioConnector.
func TestBuildMCPTools_WithConnector_HTTPTransport(t *testing.T) {
	mock := &mockListableClient{toolNames: []string{"http_tool"}}

	httpCounter := &callCountingConnector{fn: mockConnectorOf(mock)}
	stdioCounter := &callCountingConnector{fn: neverCalledConnector(t, "stdio")}

	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "remote", Transport: "http", URL: "http://example.com"},
		},
	}

	toolMap, _, err := BuildMCPToolsWithConnector(
		context.Background(), cfg,
		stdioCounter.connect, // must NOT be called
		httpCounter.connect,  // must be called once
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if httpCounter.calls != 1 {
		t.Errorf("httpConnector called %d times, expected 1", httpCounter.calls)
	}
	if stdioCounter.calls != 0 {
		t.Errorf("stdioConnector called %d times, expected 0", stdioCounter.calls)
	}
	if _, ok := toolMap["http_tool"]; !ok {
		t.Error("expected http_tool in map")
	}
}

// TestBuildMCPTools_WithConnector_UnknownTransport — transport="grpc" → server skipped, outer error nil.
func TestBuildMCPTools_WithConnector_UnknownTransport(t *testing.T) {
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{Name: "grpc-srv", Transport: "grpc"},
		},
	}
	noConnector := neverCalledConnector(t, "any")

	toolMap, _, err := BuildMCPToolsWithConnector(context.Background(), cfg, noConnector, noConnector)
	if err != nil {
		t.Fatalf("outer error must be nil for unknown transport, got: %v", err)
	}
	if len(toolMap) != 0 {
		t.Errorf("expected empty map when transport unknown, got %d tools", len(toolMap))
	}
}

// TestBuildMCPTools_WithConnector_EnvVarInjection — Env field on MCPServerConfig reaches the connector.
func TestBuildMCPTools_WithConnector_EnvVarInjection(t *testing.T) {
	mock := &mockListableClient{toolNames: []string{"tool1"}}

	var capturedCfg config.MCPServerConfig
	captureConn := mockConnectorCapture(mock, &capturedCfg)

	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{
				Name:      "env-server",
				Transport: "stdio",
				Command:   []string{"echo"},
				Env:       map[string]string{"TOKEN": "abc"},
			},
		},
	}

	_, _, err := BuildMCPToolsWithConnector(
		context.Background(), cfg,
		captureConn,
		neverCalledConnector(t, "http"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCfg.Env == nil {
		t.Fatal("Env map not propagated to connector")
	}
	if capturedCfg.Env["TOKEN"] != "abc" {
		t.Errorf("expected Env[TOKEN]=abc, got %q", capturedCfg.Env["TOKEN"])
	}
}
