package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"microagent/internal/store"
)

// BatchExecToolConfig configures the BatchExecTool.
type BatchExecToolConfig struct {
	MaxOutputBytes int           // Maximum output bytes per command (default 1MB)
	Timeout        time.Duration // Timeout per command (default 30s)
}

// BatchExecTool runs commands sequentially via Sandbox, indexes each output,
// and returns a compact summary.
type BatchExecTool struct {
	store  store.OutputStore
	config BatchExecToolConfig
}

// NewBatchExecTool creates a new BatchExecTool with the given store and config.
func NewBatchExecTool(store store.OutputStore, config BatchExecToolConfig) *BatchExecTool {
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 1024 * 1024 // 1MB default
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	return &BatchExecTool{
		store:  store,
		config: config,
	}
}

// Name returns the tool name.
func (t *BatchExecTool) Name() string {
	return "batch_exec"
}

// Description returns the tool description.
func (t *BatchExecTool) Description() string {
	return "Execute multiple shell commands sequentially. Each command's output is indexed for later search. Stop on first error if stop_on_error is true."
}

// Schema returns the JSON schema for the tool parameters.
func (t *BatchExecTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	  "type": "object",
	  "properties": {
		"commands": {
		  "type": "array",
		  "items": { "type": "string" },
		  "description": "List of shell commands to execute sequentially"
		},
		"stop_on_error": {
		  "type": "boolean",
		  "default": false,
		  "description": "Stop execution on first command that returns non-zero exit code"
		}
	  },
	  "required": ["commands"]
	}`)
}

type batchExecParams struct {
	Commands    []string `json:"commands"`
	StopOnError bool     `json:"stop_on_error"`
}

// Execute runs the commands sequentially via Sandbox, indexes each output,
// and returns a compact summary.
func (t *BatchExecTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input batchExecParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("parsing params: %v", err)}, nil
	}

	if len(input.Commands) == 0 {
		return ToolResult{IsError: true, Content: "commands array cannot be empty"}, nil
	}

	sandbox := &Sandbox{
		MaxOutputBytes: t.config.MaxOutputBytes,
		Timeout:        t.config.Timeout,
		KeepFirstN:     20,
		KeepLastN:      10,
	}

	var summaryLines []string
	var errorOccurred bool
	successCount := 0
	errorCount := 0

	for i, cmd := range input.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}

		// Execute command via sandbox
		result, err := sandbox.Run(ctx, "sh", "-c", cmd)
		if err != nil {
			// Sandbox execution error (e.g., timeout)
			errorOccurred = true
			errorCount++
			summaryLines = append(summaryLines, fmt.Sprintf("[%d] FAILED: %v", i+1, err))

			// Index the error output
			indexErr := t.store.IndexOutput(ctx, store.ToolOutput{
				ID:        fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), i),
				ToolName:  t.Name(),
				Command:   cmd,
				Content:   fmt.Sprintf("execution error: %v", err),
				Truncated: false,
				ExitCode:  -1,
				Timestamp: time.Now().UTC(),
			})
			if indexErr != nil {
				slog.Warn("batch_exec: failed to index output", "error", indexErr, "command_index", i)
			}

			if input.StopOnError {
				break
			}
			continue
		}

		// Index the output
		indexErr := t.store.IndexOutput(ctx, store.ToolOutput{
			ID:        fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), i),
			ToolName:  t.Name(),
			Command:   cmd,
			Content:   result.Output,
			Truncated: result.Metrics.Truncated,
			ExitCode:  result.Metrics.ExitCode,
			Timestamp: time.Now().UTC(),
		})
		if indexErr != nil {
			// Log but don't fail
			_ = fmt.Errorf("indexing output: %w", indexErr)
		}

		// Track success/failure
		if result.Metrics.ExitCode == 0 {
			successCount++
			// Include summary of successful commands (first few lines)
			lines := strings.Split(strings.TrimSpace(result.Output), "\n")
			preview := strings.Join(lines[:min(3, len(lines))], "; ")
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			summaryLines = append(summaryLines, fmt.Sprintf("[%d] OK: %s", i+1, preview))
		} else {
			errorOccurred = true
			errorCount++
			summaryLines = append(summaryLines, fmt.Sprintf("[%d] ERROR (exit %d): %s", i+1, result.Metrics.ExitCode, result.Summary))

			if input.StopOnError {
				break
			}
		}
	}

	// Build final summary
	summary := fmt.Sprintf("Executed %d commands: %d succeeded, %d failed\n\n%s",
		successCount+errorCount, successCount, errorCount, strings.Join(summaryLines, "\n"))

	meta := map[string]string{
		"command_count": fmt.Sprintf("%d", len(input.Commands)),
		"success_count": fmt.Sprintf("%d", successCount),
		"error_count":   fmt.Sprintf("%d", errorCount),
		"stop_on_error": fmt.Sprintf("%v", input.StopOnError),
	}

	return ToolResult{
		Content: summary,
		IsError: errorOccurred,
		Meta:    meta,
	}, nil
}
