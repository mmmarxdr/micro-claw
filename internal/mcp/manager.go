package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"microagent/internal/config"
)

// managedServer pairs a live MCP client with its config for lifecycle management.
type managedServer struct {
	cfg    config.MCPServerConfig
	client MCPCaller
}

// Manager holds all live MCP server connections and cleans them up on Close.
// The caller (main.go) must defer Manager.Close() after BuildRegistry returns.
type Manager struct {
	servers []managedServer
}

// Close terminates all managed server connections. Errors are logged at WARN
// and do not stop iteration — all servers are always attempted.
func (m *Manager) Close() {
	for _, s := range m.servers {
		if err := s.client.Close(); err != nil {
			slog.Warn("mcp: error closing server connection", "server", s.cfg.Name, "error", err)
		}
	}
}

// connectStdio spawns the MCP server subprocess and returns a connected,
// initialized client. The subprocess lifetime is tied to ctx via the
// exec.CommandContext mechanism inside the transport, plus Pdeathsig on Linux.
//
// Implementation note: mark3labs/mcp-go v0.45.0 exposes transport.WithCommandFunc,
// a StdioOption that receives a CommandFunc factory. This allows us to intercept
// the exec.Cmd before it starts and call setPdeathsig() for belt-and-suspenders
// subprocess cleanup on Linux.
func connectStdio(ctx context.Context, cfg config.MCPServerConfig) (MCPCaller, error) {
	// WithCommandFunc intercepts subprocess creation so we can set Pdeathsig.
	cmdFuncOpt := transport.WithCommandFunc(func(cmdCtx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(cmdCtx, command, args...)
		cmd.Env = append(cmd.Env, env...)
		setPdeathsig(cmd)
		return cmd, nil
	})

	c, err := client.NewStdioMCPClientWithOptions(cfg.Command[0], nil, cfg.Command[1:], cmdFuncOpt)
	if err != nil {
		return nil, fmt.Errorf("create stdio client for %q: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "micro-claw", Version: "dev"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize stdio server %q: %w", cfg.Name, err)
	}
	return c, nil
}

// connectHTTP connects to a remote MCP server over SSE/HTTP and returns a
// connected, initialized client. The HTTP SSE stream is started before
// Initialize is called, as required by the mcp-go client API.
func connectHTTP(ctx context.Context, cfg config.MCPServerConfig) (MCPCaller, error) {
	c, err := client.NewSSEMCPClient(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("create http client for %q: %w", cfg.Name, err)
	}

	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("start http client for %q: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "micro-claw", Version: "dev"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize http server %q: %w", cfg.Name, err)
	}
	return c, nil
}
