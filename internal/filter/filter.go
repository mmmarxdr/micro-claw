package filter

import (
	"encoding/json"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// FilterFunc transforms tool output content.
// Implementations must not modify the input ToolResult in place; they must
// return a new value. The string return is the FilterName for metrics.
type FilterFunc func(input json.RawMessage, result tool.ToolResult, cfg config.FilterConfig) (tool.ToolResult, string)

// Apply post-processes a tool result before it enters the conversation context.
// It is a zero-allocation no-op when cfg.Enabled is false.
// Error results (result.IsError == true) are never filtered.
func Apply(toolName string, input json.RawMessage, result tool.ToolResult, cfg config.FilterConfig) (tool.ToolResult, Metrics) {
	if !cfg.Enabled || result.IsError {
		return result, Metrics{}
	}

	orig := len(result.Content)

	var (
		filtered string
		name     string
	)

	switch toolName {
	case "shell_exec":
		filtered, name = applyShell(input, result.Content, cfg)

	case "read_file":
		var rp struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(input, &rp)
		filtered, name = FilterFileContent(rp.Path, result.Content, cfg.Levels.FileRead)

	case "list_files":
		filtered, name = FormatListing(result.Content)

	case "http_fetch":
		filtered, name = FilterHTTP(result.Content, cfg.TruncationChars)

	case "write_file":
		// Write confirmations are never filtered.
		return result, Metrics{}

	default:
		// MCP tools and unrecognised native tools: apply generic truncation only
		// if content exceeds the limit and generic truncation is enabled.
		if cfg.Levels.Generic && cfg.TruncationChars > 0 {
			filtered, name = Truncate(result.Content, cfg.TruncationChars)
		} else {
			return result, Metrics{}
		}
	}

	out := result
	out.Content = filtered
	return out, Metrics{
		OriginalBytes:   orig,
		CompressedBytes: len(filtered),
		FilterName:      name,
	}
}
