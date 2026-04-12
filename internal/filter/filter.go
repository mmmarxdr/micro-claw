package filter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// FilterFunc transforms tool output content.
// Implementations must not modify the input ToolResult in place; they must
// return a new value. The string return is the FilterName for metrics.
type FilterFunc func(input json.RawMessage, result tool.ToolResult, cfg config.FilterConfig) (tool.ToolResult, string)

// PreExecuteFunc is a hook that can run before tool execution.
// When context‑mode is enabled, it can inspect the input and configuration
// to decide whether to short‑circuit execution.
// Returns (result, true) to skip execution, (_, false) to proceed.
type PreExecuteFunc func(input json.RawMessage, cfg config.ContextModeConfig) (tool.ToolResult, bool)

// PreApply runs before tool execution.
// When context‑mode is enabled (auto|conservative), it can intercept the call
// and return (result, true) to skip the actual tool execution.
// Returns (result, false) to let execution proceed normally.
// The ctx parameter is propagated to any sandboxed sub-process so that
// parent cancellation/timeout reaches commands started here.
func PreApply(ctx context.Context, toolName string, input json.RawMessage, cfg config.ContextModeConfig) (tool.ToolResult, bool) {
	// If context-mode is off, never intercept
	if cfg.Mode == config.ContextModeOff {
		return tool.ToolResult{}, false
	}

	// Handle supported tools
	switch toolName {
	case "shell_exec":
		return preApplyShell(ctx, input, cfg)
	default:
		// Unsupported tool - continue execution
		return tool.ToolResult{}, false
	}
}

// preApplyShell handles shell_exec tool pre-execution.
// When context-mode is enabled, intercepts and runs via BoundedExec with byte limiting.
func preApplyShell(ctx context.Context, input json.RawMessage, cfg config.ContextModeConfig) (tool.ToolResult, bool) {
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tool.ToolResult{}, false
	}

	if params.Command == "" {
		return tool.ToolResult{}, false
	}

	// Create bounded executor with context-mode limits.
	be := &tool.BoundedExec{
		MaxOutputBytes: cfg.ShellMaxOutput,
		Timeout:        cfg.SandboxTimeout,
		KeepFirstN:     cfg.SandboxKeepFirst,
		KeepLastN:      cfg.SandboxKeepLast,
	}

	result, err := be.Run(ctx, "sh", "-c", params.Command)
	if err != nil {
		// BoundedExec.Run always returns nil; this branch is kept for interface safety.
		return tool.ToolResult{
			IsError: true,
			Content: fmt.Sprintf("sandbox execution failed: %v", err),
			Meta: map[string]string{
				"command":                  params.Command,
				"microagent/exit_code":     "-1",
				"microagent/error_kind":    "other",
				"microagent/truncated":     "false",
				"microagent/presummarized": "true",
			},
		}, true
	}

	// Build result from bounded exec output.
	exitCode := fmt.Sprintf("%d", result.Metrics.ExitCode)
	truncated := fmt.Sprintf("%v", result.Metrics.Truncated)
	meta := map[string]string{
		"command":                  params.Command,
		"microagent/exit_code":     exitCode,
		"microagent/truncated":     truncated,
		"microagent/presummarized": "true",
	}
	if result.Metrics.ErrorKind != tool.ExecErrorNone {
		meta["microagent/error_kind"] = string(result.Metrics.ErrorKind)
	}

	content := result.Summary
	if len(strings.TrimSpace(content)) == 0 {
		content = "(command successful, no output)"
	}

	isError := result.Metrics.ExitCode != 0
	if isError {
		content = fmt.Sprintf("Command failed (exit %d)\nOutput: %s", result.Metrics.ExitCode, content)
	}

	return tool.ToolResult{
		Content: content,
		IsError: isError,
		Meta:    meta,
	}, true // true = skip normal execution
}

// Apply post-processes a tool result before it enters the conversation context.
// It is a zero-allocation no-op when cfg.Enabled is false.
// Error results (result.IsError == true) are never filtered.
// Results that were already summarised by PreApply (Meta["microagent/presummarized"]=="true")
// are returned unchanged so they are never double-processed.
// TODO(multimodal-tool-output): filter operates on text tool output only; content blocks live in context.go
func Apply(toolName string, input json.RawMessage, result tool.ToolResult, cfg config.FilterConfig) (tool.ToolResult, Metrics) {
	if result.Meta["microagent/presummarized"] == "true" {
		return result, Metrics{}
	}
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
