package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// echoHandler returns the "message" argument as text content (IsError: false).
func echoHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	msg, _ := req.GetArguments()["message"].(string)
	return mcp.NewToolResultText(msg), nil
}

// errorHandler always returns an MCP-level error response (IsError: true).
func errorHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError("error_tool: simulated MCP error"), nil
}

func main() {
	mcpServer := server.NewMCPServer("test-server", "0.1.0")

	echoTool := mcp.NewTool(
		"echo_tool",
		mcp.WithDescription("Returns its message argument"),
		mcp.WithString("message", mcp.Required()),
	)
	errorTool := mcp.NewTool(
		"error_tool",
		mcp.WithDescription("Always returns an MCP error"),
	)

	mcpServer.AddTool(echoTool, echoHandler)
	mcpServer.AddTool(errorTool, errorHandler)

	//nolint:errcheck // binary exits on stdin close; error is not actionable
	_ = server.ServeStdio(mcpServer)
}
