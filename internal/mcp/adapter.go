package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"daimon/internal/tool"
)

// MCPCaller abstracts the subset of client.Client used by MCPToolAdapter.
// Defined in the consumer package (internal/mcp) per Go idiom — the interface
// is satisfied by *client.Client in production and mockable in tests.
type MCPCaller interface {
	CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	Close() error
}

// MCPToolAdapter adapts a single MCP server tool into the tool.Tool interface.
// It holds an MCPCaller (the shared client connection to the MCP server) and
// the tool definition returned by ListTools().
type MCPToolAdapter struct {
	caller     MCPCaller
	toolDef    mcp.Tool
	remoteName string // original tool name as known by the MCP server (without prefix)
}

// Name returns the tool's name as declared by the MCP server (with optional prefix applied).
func (a *MCPToolAdapter) Name() string {
	return a.toolDef.Name
}

// Description returns the tool's description as declared by the MCP server.
func (a *MCPToolAdapter) Description() string {
	return a.toolDef.Description
}

// Schema marshals the tool's input schema to JSON. If marshalling fails,
// a safe fallback schema is returned and a warning is logged.
func (a *MCPToolAdapter) Schema() json.RawMessage {
	raw, err := json.Marshal(a.toolDef.InputSchema)
	if err != nil {
		slog.Warn("mcp: failed to marshal tool schema", "tool", a.toolDef.Name, "error", err)
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

// Execute invokes the remote MCP tool and converts the result to tool.ToolResult.
//
// Error translation policy:
//   - MCP-level error (result.IsError == true): returned as ToolResult{IsError: true},
//     no Go error — the agent loop treats it as tool output, not a fatal failure.
//   - Transport error (CallTool returns Go error): returned as (ToolResult{}, err) —
//     the agent loop handles it as a structural tool failure.
//   - Invalid params JSON: returned as Go error before calling the server.
func (a *MCPToolAdapter) Execute(ctx context.Context, params json.RawMessage) (tool.ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(params, &args); err != nil {
		return tool.ToolResult{}, fmt.Errorf("mcp tool %q: unmarshal params: %w", a.toolDef.Name, err)
	}

	// Use remoteName (the original unprefixed name) when calling the MCP server.
	// toolDef.Name may have a prefix added for the agent's tool registry.
	callName := a.remoteName
	if callName == "" {
		callName = a.toolDef.Name // fallback for adapters created without remoteName
	}
	result, err := a.caller.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      callName,
			Arguments: args,
		},
	})
	if err != nil {
		return tool.ToolResult{}, fmt.Errorf("mcp tool %q: call failed: %w", a.toolDef.Name, err)
	}

	content := extractText(result.Content)
	return tool.ToolResult{
		Content: content,
		IsError: result.IsError,
	}, nil
}

// extractText concatenates the text from MCP content items.
// Non-text content types (image, audio, embedded resource) are represented
// as a placeholder — they cannot be surfaced meaningfully to LLMs as text.
func extractText(contents []mcp.Content) string {
	var sb strings.Builder
	for _, c := range contents {
		switch v := c.(type) {
		case mcp.TextContent:
			sb.WriteString(v.Text)
		default:
			sb.WriteString("[non-text content]")
		}
	}
	return sb.String()
}
