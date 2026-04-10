package tool

import (
	"microagent/internal/config"
	"microagent/internal/store"
)

// BuildRegistry constructs the built-in tool map from config.
// MCP tools are merged in by the caller (main.go) via MergeTools,
// which avoids an import cycle between internal/tool and internal/mcp.
// When ctxModeCfg.Mode is not "off" and outputStore is provided, it also
// registers BatchExecTool and SearchOutputTool for context-mode features.
func BuildRegistry(cfg config.ToolsConfig, ctxModeCfg config.ContextModeConfig, outputStore store.OutputStore) map[string]Tool {
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

	// Register context-mode tools when enabled
	if ctxModeCfg.Mode != config.ContextModeOff && outputStore != nil {
		// Register BatchExecTool
		batchCfg := BatchExecToolConfig{
			MaxOutputBytes: ctxModeCfg.ShellMaxOutput * 2, // Allow some buffer
			Timeout:        ctxModeCfg.SandboxTimeout,
		}
		batchTool := NewBatchExecTool(outputStore, batchCfg)
		registry[batchTool.Name()] = batchTool

		// Register SearchOutputTool
		searchTool := NewSearchOutputTool(outputStore)
		registry[searchTool.Name()] = searchTool
	}

	return registry
}

// BuildRegistrySimple constructs the built-in tool map from config without
// context-mode tools. This is a convenience function for callers that don't
// need context-mode features.
func BuildRegistrySimple(cfg config.ToolsConfig) map[string]Tool {
	return BuildRegistry(cfg, config.ContextModeConfig{}, nil)
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
