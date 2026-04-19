package tui

import (
	"strings"

	"daimon/internal/config"
)

// MCPTabData holds the MCP configuration snapshot for the dashboard MCP tab.
// Populated from config at dashboard init time — no live connection is attempted.
type MCPTabData struct {
	Enabled bool
	Timeout string // formatted duration, e.g. "10s" or "10s (default)"
	Servers []MCPServerRow
}

// MCPServerRow is a display-ready row for a single MCP server.
type MCPServerRow struct {
	Name         string
	Transport    string
	CommandOrURL string // strings.Join(cmd, " ") for stdio, URL for http
	PrefixTools  bool
	EnvCount     int // number of env vars configured
}

// loadMCPData extracts MCPTabData from a config without making any network calls.
// Calling loadMCPData(nil) is safe and returns a zero-value MCPTabData.
func loadMCPData(cfg *config.Config) MCPTabData {
	if cfg == nil {
		return MCPTabData{}
	}
	mcpCfg := cfg.Tools.MCP

	rows := make([]MCPServerRow, 0, len(mcpCfg.Servers))
	for _, srv := range mcpCfg.Servers {
		var commandOrURL string
		switch srv.Transport {
		case "stdio":
			if len(srv.Command) > 0 {
				commandOrURL = strings.Join(srv.Command, " ")
			}
		case "http":
			commandOrURL = srv.URL
		}
		rows = append(rows, MCPServerRow{
			Name:         srv.Name,
			Transport:    srv.Transport,
			CommandOrURL: commandOrURL,
			PrefixTools:  srv.PrefixTools,
			EnvCount:     len(srv.Env),
		})
	}

	timeout := mcpCfg.ConnectTimeout.String()
	if mcpCfg.ConnectTimeout == 0 {
		timeout = "10s (default)"
	}

	return MCPTabData{
		Enabled: mcpCfg.Enabled,
		Timeout: timeout,
		Servers: rows,
	}
}
