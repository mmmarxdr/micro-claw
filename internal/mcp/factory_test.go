package mcp

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"microagent/internal/config"
	"microagent/internal/tool"
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

func TestBuildMCPTools_EmptyManagerAfterAllFail(_ *testing.T) {
	// This test documents the behavior: if a server fails to connect,
	// its client is not added to the Manager. The Manager.Close() must
	// still be safe to call (no-op on empty servers).
	manager := &Manager{}
	manager.Close() // must not panic
}
