package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"microagent/internal/config"
)

func resolvePath(basePath, reqPath string) (string, error) {
	if strings.HasPrefix(basePath, "~") {
		if usr, err := user.Current(); err == nil {
			basePath = strings.Replace(basePath, "~", usr.HomeDir, 1)
		}
	}
	base, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return "", err
	}

	targetPath := filepath.Join(base, filepath.Clean(reqPath))
	targetPath, err = filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(targetPath, base) {
		return "", fmt.Errorf("path traversal attempt rejected")
	}

	return targetPath, nil
}

func parseSize(s string) int64 {
	s = strings.ToUpper(strings.TrimSpace(s))
	if strings.HasSuffix(s, "MB") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(s, "MB"), 10, 64)
		return val * 1024 * 1024
	}
	if strings.HasSuffix(s, "KB") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(s, "KB"), 10, 64)
		return val * 1024
	}
	val, _ := strconv.ParseInt(s, 10, 64)
	return val
}

// ReadFileTool
type ReadFileTool struct {
	config config.FileToolConfig
}

func NewReadFileTool(cfg config.FileToolConfig) *ReadFileTool {
	return &ReadFileTool{config: cfg}
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read the contents of a file. Path is relative to the configured base_path."
}

func (t *ReadFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Relative file path to read" }
  },
  "required": ["path"]
}`)
}

type readParams struct {
	Path string `json:"path"`
}

func (t *ReadFileTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input readParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, fmt.Errorf("parsing params: %w", err)
	}

	target, err := resolvePath(t.config.BasePath, input.Path)
	if err != nil {
		return ToolResult{IsError: true, Content: err.Error()}, nil
	}

	info, err := os.Stat(target)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to stat file: %v", err)}, nil
	}

	maxSize := parseSize(t.config.MaxFileSize)
	if maxSize > 0 && info.Size() > maxSize {
		return ToolResult{IsError: true, Content: fmt.Sprintf("file size %d exceeds maximum allowed %s", info.Size(), t.config.MaxFileSize)}, nil
	}

	data, err := os.ReadFile(target)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to read file: %v", err)}, nil
	}

	return ToolResult{Content: string(data)}, nil
}

// WriteFileTool
type WriteFileTool struct {
	config config.FileToolConfig
}

func NewWriteFileTool(cfg config.FileToolConfig) *WriteFileTool {
	return &WriteFileTool{config: cfg}
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Write content to a file. Creates parent directories if needed. Path is relative to the configured base_path."
}

func (t *WriteFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Relative file path to write" },
    "content": { "type": "string", "description": "Content to write to the file" }
  },
  "required": ["path", "content"]
}`)
}

type writeParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteFileTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input writeParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, fmt.Errorf("parsing params: %w", err)
	}

	target, err := resolvePath(t.config.BasePath, input.Path)
	if err != nil {
		return ToolResult{IsError: true, Content: err.Error()}, nil
	}

	maxSize := parseSize(t.config.MaxFileSize)
	if maxSize > 0 && int64(len(input.Content)) > maxSize {
		return ToolResult{IsError: true, Content: fmt.Sprintf("content size %d exceeds maximum allowed %s", len(input.Content), t.config.MaxFileSize)}, nil
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to create directories: %v", err)}, nil
	}

	if err := os.WriteFile(target, []byte(input.Content), 0o644); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to write file: %v", err)}, nil
	}

	return ToolResult{Content: fmt.Sprintf("Successfully wrote to %s", input.Path)}, nil
}

// ListFilesTool
type ListFilesTool struct {
	config config.FileToolConfig
}

func NewListFilesTool(cfg config.FileToolConfig) *ListFilesTool {
	return &ListFilesTool{config: cfg}
}

func (t *ListFilesTool) Name() string { return "list_files" }
func (t *ListFilesTool) Description() string {
	return "List files and directories at the given path. Path is relative to the configured base_path."
}

func (t *ListFilesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": { "type": "string", "description": "Relative directory path to list (default: '.')" }
  }
}`)
}

type listParams struct {
	Path string `json:"path"`
}

func (t *ListFilesTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input listParams
	if len(params) > 0 && string(params) != "{}" && string(params) != "null" {
		if err := json.Unmarshal(params, &input); err != nil {
			return ToolResult{}, fmt.Errorf("parsing params: %w", err)
		}
	}

	if input.Path == "" {
		input.Path = "."
	}

	target, err := resolvePath(t.config.BasePath, input.Path)
	if err != nil {
		return ToolResult{IsError: true, Content: err.Error()}, nil
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to list directory: %v", err)}, nil
	}

	var out strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}

		marker := ""
		if e.IsDir() {
			marker = "/"
		}

		out.WriteString(fmt.Sprintf("%s%s (%d bytes)\n", e.Name(), marker, info.Size()))
	}

	if out.Len() == 0 {
		return ToolResult{Content: "(empty directory)"}, nil
	}

	return ToolResult{Content: out.String()}, nil
}
