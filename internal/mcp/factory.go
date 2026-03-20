package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/sync/errgroup"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// listableClient is satisfied by *client.Client in production and by mock
// types in tests. It extends MCPCaller with the ListTools discovery method,
// eliminating the need for a type-assertion to *client.Client in the factory.
type listableClient interface {
	MCPCaller
	ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
}

// ConnectorFunc is the function type for connecting to a single MCP server.
// It is used by BuildMCPToolsWithConnector to allow test injection of mock
// connectors without spawning real subprocesses or network connections.
// The returned listableClient must be closed by the caller when done.
type ConnectorFunc func(ctx context.Context, cfg config.MCPServerConfig) (listableClient, error)

// serverResult carries the outcome of one concurrent server connection attempt.
// Errors captured here are non-fatal — the agent starts regardless.
type serverResult struct {
	cfg   config.MCPServerConfig
	tools []tool.Tool
	err   error
}

// BuildMCPTools connects to all configured MCP servers concurrently and returns
// a map of tool name → tool.Tool for every successfully discovered remote tool,
// plus a *Manager the caller must Close() on shutdown.
//
// Failure policy: individual server failures are logged at WARN and skipped —
// they never prevent the agent from starting. The outer error is reserved for
// truly fatal pre-flight issues (none anticipated given config validation).
func BuildMCPTools(ctx context.Context, cfg config.MCPConfig) (map[string]tool.Tool, *Manager, error) {
	return BuildMCPToolsWithConnector(ctx, cfg, connectStdioListable, connectHTTPListable)
}

// connectStdioListable wraps connectStdio to return a listableClient.
func connectStdioListable(ctx context.Context, cfg config.MCPServerConfig) (listableClient, error) {
	c, err := connectStdio(ctx, cfg)
	if err != nil {
		return nil, err
	}
	lc, ok := c.(listableClient)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("stdio client for %q does not implement ListTools", cfg.Name)
	}
	return lc, nil
}

// connectHTTPListable wraps connectHTTP to return a listableClient.
func connectHTTPListable(ctx context.Context, cfg config.MCPServerConfig) (listableClient, error) {
	c, err := connectHTTP(ctx, cfg)
	if err != nil {
		return nil, err
	}
	lc, ok := c.(listableClient)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("http client for %q does not implement ListTools", cfg.Name)
	}
	return lc, nil
}

// BuildMCPToolsWithConnector is the testable core. It accepts injectable
// connector functions for both transports, enabling unit tests to mock
// network/subprocess behaviour without spawning real processes.
//
// All existing callers use BuildMCPTools which delegates here with the real
// connectStdio/connectHTTP functions — the public API is unchanged.
func BuildMCPToolsWithConnector(
	ctx context.Context,
	cfg config.MCPConfig,
	stdioConnector ConnectorFunc,
	httpConnector ConnectorFunc,
) (map[string]tool.Tool, *Manager, error) {
	if !cfg.Enabled {
		return nil, &Manager{}, nil
	}

	if len(cfg.Servers) == 0 {
		return make(map[string]tool.Tool), &Manager{}, nil
	}

	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	results := make([]serverResult, len(cfg.Servers))
	manager := &Manager{}
	// Mutex-free: each goroutine writes only to its own results[i] slot.
	// manager.servers is appended inside the goroutine — use a separate
	// per-server list and consolidate after g.Wait() to avoid a data race.
	serverSlots := make([]managedServer, len(cfg.Servers))
	connected := make([]bool, len(cfg.Servers))

	var g errgroup.Group
	// Note: we intentionally use a plain errgroup (not errgroup.WithContext) so
	// that one server's failure does NOT cancel connections to healthy servers.

	for i, srv := range cfg.Servers {
		i, srv := i, srv // capture loop variables
		g.Go(func() error {
			connectCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			var caller listableClient
			var err error

			switch srv.Transport {
			case "stdio":
				caller, err = stdioConnector(connectCtx, srv)
			case "http":
				caller, err = httpConnector(connectCtx, srv)
			default:
				// Should not reach here — config.validate() rejects unknown transports.
				results[i] = serverResult{cfg: srv, err: fmt.Errorf("unknown transport %q", srv.Transport)}
				return nil // non-fatal
			}

			if err != nil {
				results[i] = serverResult{cfg: srv, err: err}
				return nil // non-fatal
			}

			listResult, err := caller.ListTools(connectCtx, mcp.ListToolsRequest{})
			if err != nil {
				_ = caller.Close()
				results[i] = serverResult{cfg: srv, err: fmt.Errorf("list tools from %q: %w", srv.Name, err)}
				return nil
			}

			adapters := make([]tool.Tool, 0, len(listResult.Tools))
			for _, t := range listResult.Tools {
				toolName := t.Name
				if srv.PrefixTools {
					toolName = srv.Name + "_" + t.Name
				}
				adapters = append(adapters, &MCPToolAdapter{
					caller: caller,
					// Store a copy of the tool def with the (possibly prefixed) name.
					toolDef: mcp.Tool{
						Name:        toolName,
						Description: t.Description,
						InputSchema: t.InputSchema,
					},
				})
			}

			results[i] = serverResult{cfg: srv, tools: adapters}
			serverSlots[i] = managedServer{cfg: srv, client: caller}
			connected[i] = true
			slog.Info("mcp: server connected", "server", srv.Name, "tools", len(adapters))
			return nil
		})
	}

	_ = g.Wait() // g.Go always returns nil; errors are captured in results

	// Consolidate connected servers into Manager (preserve order).
	for i, ok := range connected {
		if ok {
			manager.servers = append(manager.servers, serverSlots[i])
		}
	}

	// Assemble the tool map. Policy:
	//   - Failed servers: log WARN, skip.
	//   - Inter-MCP name collision: first-writer-wins (log WARN for duplicates).
	toolMap := make(map[string]tool.Tool)
	for _, r := range results {
		if r.err != nil {
			slog.Warn("mcp: server connection failed", "server", r.cfg.Name, "error", r.err)
			continue
		}
		for _, t := range r.tools {
			if _, exists := toolMap[t.Name()]; exists {
				slog.Warn("mcp: duplicate tool name across servers, first wins",
					"tool", t.Name(), "server", r.cfg.Name)
				continue
			}
			toolMap[t.Name()] = t
		}
	}

	return toolMap, manager, nil
}
