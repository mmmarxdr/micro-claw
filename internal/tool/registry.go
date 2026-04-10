package tool

import (
	"microagent/internal/config"
)

// BuildRegistry constructs the built-in tool map from config.
// MCP tools are merged in by the caller (main.go) via MergeTools,
// which avoids an import cycle between internal/tool and internal/mcp.
// Context-mode tools (BatchExecTool, SearchOutputTool) are registered
// directly in main.go after the store is created.
func BuildRegistry(cfg config.ToolsConfig) map[string]Tool {
	registry := make(map[string]Tool)

	if cfg.Shell.Enabled {
		st := NewShellTool(cfg.Shell)
		registry[st.Name()] = st
	}

	if cfg.File.Enabled {
		rt := NewReadFileTool(cfg.File)
		wt := NewWriteFileTool(cfg.File)
		lt := NewListFilesTool(cfg.File)
		registry[rt.Name()] = rt
		registry[wt.Name()] = wt
		registry[lt.Name()] = lt
	}

	if cfg.HTTP.Enabled {
		ht := NewHTTPFetchTool(cfg.HTTP)
		registry[ht.Name()] = ht
	}

	return registry
}

// BuildRegistrySimple is an alias for BuildRegistry kept for backward
// compatibility with existing tests and callers.
func BuildRegistrySimple(cfg config.ToolsConfig) map[string]Tool {
	return BuildRegistry(cfg)
}

// MergeTools merges external tools (e.g. from MCP) into an existing registry.
// Built-ins already in the registry take precedence — MCP tools whose names
// collide with built-ins are skipped (the caller logs the warning before calling).
// This function does not log; callers are responsible for collision warnings.
func MergeTools(registry map[string]Tool, external map[string]Tool) {
	for name, t := range external {
		if _, exists := registry[name]; !exists {
			registry[name] = t
		}
	}
}
