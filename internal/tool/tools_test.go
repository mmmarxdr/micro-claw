package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
)

// ---------------------------------------------------------------------------
// TestBuildRegistry
// ---------------------------------------------------------------------------

func TestBuildRegistry(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.ToolsConfig
		wantLen  int
		wantKeys []string
	}{
		{
			name:    "all disabled returns empty map",
			cfg:     config.ToolsConfig{},
			wantLen: 0,
		},
		{
			name: "shell only returns 1 tool",
			cfg: config.ToolsConfig{
				Shell: config.ShellToolConfig{Enabled: true},
			},
			wantLen:  1,
			wantKeys: []string{"shell_exec"},
		},
		{
			name: "file only returns 3 tools",
			cfg: config.ToolsConfig{
				File: config.FileToolConfig{Enabled: true},
			},
			wantLen:  3,
			wantKeys: []string{"read_file", "write_file", "list_files"},
		},
		{
			name: "http only returns 1 tool",
			cfg: config.ToolsConfig{
				HTTP: config.HTTPToolConfig{Enabled: true},
			},
			wantLen:  1,
			wantKeys: []string{"http_fetch"},
		},
		{
			name: "all enabled returns 5 tools",
			cfg: config.ToolsConfig{
				Shell: config.ShellToolConfig{Enabled: true},
				File:  config.FileToolConfig{Enabled: true},
				HTTP:  config.HTTPToolConfig{Enabled: true},
			},
			wantLen:  5,
			wantKeys: []string{"shell_exec", "read_file", "write_file", "list_files", "http_fetch"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := BuildRegistrySimple(tc.cfg)
			if len(reg) != tc.wantLen {
				t.Errorf("got %d tools, want %d", len(reg), tc.wantLen)
			}
			for _, key := range tc.wantKeys {
				if _, ok := reg[key]; !ok {
					t.Errorf("expected key %q in registry, not found", key)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestShellTool_Interface
// ---------------------------------------------------------------------------

func TestShellTool_Interface(t *testing.T) {
	st := NewShellTool(config.ShellToolConfig{AllowAll: true})

	t.Run("Name returns shell_exec", func(t *testing.T) {
		if st.Name() != "shell_exec" {
			t.Errorf("got %q, want %q", st.Name(), "shell_exec")
		}
	})

	t.Run("Description is non-empty", func(t *testing.T) {
		if st.Description() == "" {
			t.Error("Description() returned empty string")
		}
	})

	t.Run("Schema is valid JSON", func(t *testing.T) {
		schema := st.Schema()
		if !json.Valid(schema) {
			t.Errorf("Schema() is not valid JSON: %s", schema)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(schema, &m); err != nil {
			t.Errorf("failed to parse schema: %v", err)
		}
		props, ok := m["properties"].(map[string]interface{})
		if !ok {
			t.Error("schema missing 'properties' key")
		} else if _, ok := props["command"]; !ok {
			t.Error("schema missing 'command' property")
		}
	})
}

// ---------------------------------------------------------------------------
// TestShellTool_Execute
// ---------------------------------------------------------------------------

func TestShellTool_Execute(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	tests := []struct {
		name      string
		cfg       config.ShellToolConfig
		params    string
		wantIsErr bool
		wantStr   string
		wantGoErr bool
	}{
		{
			name:      "whitelisted command succeeds",
			cfg:       config.ShellToolConfig{AllowedCommands: []string{"echo"}, AllowAll: false},
			params:    `{"command":"echo hello"}`,
			wantIsErr: false,
			wantStr:   "hello",
		},
		{
			name:      "non-whitelisted command returns IsError",
			cfg:       config.ShellToolConfig{AllowedCommands: []string{"ls"}, AllowAll: false},
			params:    `{"command":"rm -rf /"}`,
			wantIsErr: true,
			wantStr:   "not in the allowed list",
		},
		{
			name:      "allow_all bypasses whitelist",
			cfg:       config.ShellToolConfig{AllowAll: true},
			params:    `{"command":"echo secret"}`,
			wantIsErr: false,
			wantStr:   "secret",
		},
		{
			name:      "empty command returns IsError",
			cfg:       config.ShellToolConfig{AllowAll: true},
			params:    `{"command":""}`,
			wantIsErr: true,
			wantStr:   "cannot be empty",
		},
		{
			name:      "command failure returns IsError",
			cfg:       config.ShellToolConfig{AllowAll: true},
			params:    `{"command":"ls /nonexistent_path_12345_xyz"}`,
			wantIsErr: true,
			wantStr:   "Command failed",
		},
		{
			name:      "zero output returns no output message",
			cfg:       config.ShellToolConfig{AllowAll: true},
			params:    `{"command":"true"}`,
			wantIsErr: false,
			wantStr:   "(command successful, no output)",
		},
		{
			name:      "invalid JSON params returns Go error",
			cfg:       config.ShellToolConfig{AllowAll: true},
			params:    `{bad json`,
			wantGoErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := NewShellTool(tc.cfg)
			ctx := context.Background()
			result, err := st.Execute(ctx, json.RawMessage(tc.params))
			if tc.wantGoErr {
				if err == nil {
					t.Error("expected Go error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.IsError != tc.wantIsErr {
				t.Errorf("IsError = %v, want %v; content: %q", result.IsError, tc.wantIsErr, result.Content)
			}
			if tc.wantStr != "" && !strings.Contains(result.Content, tc.wantStr) {
				t.Errorf("content %q does not contain %q", result.Content, tc.wantStr)
			}
		})
	}
}

func TestShellTool_ContextTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	st := NewShellTool(config.ShellToolConfig{AllowAll: true})
	// Use a short deadline; the command loops without forking to avoid pipe-hold issues.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Use a shell while-loop (no subprocess fork) so that killing sh ends the I/O immediately.
	result, err := st.Execute(ctx, json.RawMessage(`{"command":"while true; do :; done"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for timed-out command, content: %q", result.Content)
	}
	if result.Content != "Tool timed out" {
		t.Errorf("expected 'Tool timed out', got %q", result.Content)
	}
}

func TestShellTool_OutputTruncation(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	st := NewShellTool(config.ShellToolConfig{AllowAll: true})
	ctx := context.Background()

	// produce >64KB output so the shell-tool cap triggers
	result, err := st.Execute(ctx, json.RawMessage(`{"command":"printf '%070000d' 1"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected IsError=true, content: %q", result.Content)
	}
	if !strings.Contains(result.Content, "...(output truncated — showing first") {
		t.Errorf("expected truncation marker with byte counts, got content ending: %q", result.Content[max(0, len(result.Content)-80):])
	}
	// The marker includes variable-length byte counts; cap the sanity check at a
	// generous upper bound (body + ~200 char marker).
	const maxLen = 64*1024 + 200
	if len(result.Content) > maxLen {
		t.Errorf("truncated content still too large: %d bytes", len(result.Content))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestShellTool_WorkingDir(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	tmpDir := t.TempDir()
	st := NewShellTool(config.ShellToolConfig{AllowAll: true, WorkingDir: tmpDir})
	ctx := context.Background()

	result, err := st.Execute(ctx, json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected IsError=true, content: %q", result.Content)
	}
	// The tmpDir may resolve to a different absolute path (e.g., symlinks); use real path
	realTmpDir, _ := filepath.EvalSymlinks(tmpDir)
	if !strings.Contains(result.Content, realTmpDir) && !strings.Contains(result.Content, tmpDir) {
		t.Errorf("pwd output %q does not contain working dir %q", result.Content, tmpDir)
	}
}

// ---------------------------------------------------------------------------
// TestFileToolInterfaces — Description and Schema for file tools and HTTPFetch
// ---------------------------------------------------------------------------

func TestFileToolInterfaces(t *testing.T) {
	dir := t.TempDir()
	cfg := config.FileToolConfig{BasePath: dir}
	httpCfg := config.HTTPToolConfig{Timeout: 5 * time.Second}

	tools := []Tool{
		NewReadFileTool(cfg),
		NewWriteFileTool(cfg),
		NewListFilesTool(cfg),
		NewHTTPFetchTool(httpCfg),
	}

	for _, tl := range tools {
		t.Run(tl.Name()+"_description_non_empty", func(t *testing.T) {
			if tl.Description() == "" {
				t.Errorf("%s.Description() returned empty string", tl.Name())
			}
		})
		t.Run(tl.Name()+"_schema_valid_json", func(t *testing.T) {
			if !json.Valid(tl.Schema()) {
				t.Errorf("%s.Schema() is not valid JSON", tl.Name())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestResolvePath
// ---------------------------------------------------------------------------

func TestResolvePath(t *testing.T) {
	base := t.TempDir()

	tests := []struct {
		name    string
		reqPath string
		wantErr bool
		errStr  string
	}{
		{
			name:    "normal relative path resolves under base",
			reqPath: "subdir/file.txt",
			wantErr: false,
		},
		{
			name:    "traversal is rejected",
			reqPath: "../../etc/passwd",
			wantErr: true,
			errStr:  "path traversal attempt rejected",
		},
		{
			// On Linux, filepath.Join(base, filepath.Clean("/etc/passwd")) = base+"/etc/passwd"
			// which has the base prefix → resolvePath sandboxes it (no error).
			// The traversal rejection only catches paths that escape the base after joining.
			name:    "absolute path sandboxed under base (no error)",
			reqPath: "/etc/passwd",
			wantErr: false,
		},
		{
			name:    "dotdot in middle of path still resolves safely",
			reqPath: "a/../b/file.txt",
			wantErr: false,
		},
		{
			name:    "empty reqPath resolves to base",
			reqPath: "",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePath(base, tc.reqPath)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil; result: %q", got)
					return
				}
				if tc.errStr != "" && !strings.Contains(err.Error(), tc.errStr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errStr)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if !strings.HasPrefix(got, base) {
				t.Errorf("resolved path %q does not start with base %q", got, base)
			}
		})
	}
}

func TestResolvePath_TildeBase(t *testing.T) {
	// Exercise the tilde expansion branch in resolvePath
	// "~" alone should resolve to the home directory
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}
	got, err := resolvePath("~", "somefile.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, home) {
		t.Errorf("resolved path %q does not start with home %q", got, home)
	}
}

// ---------------------------------------------------------------------------
// TestParseSize
// ---------------------------------------------------------------------------

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"2MB", 2 * 1024 * 1024},
		{"512KB", 512 * 1024},
		{"1024", 1024},
		{"5GB", 0}, // unknown suffix → 0
		{"", 0},
		{"100KB", 100 * 1024},
		{"5MB", 5 * 1024 * 1024},
		{"500", 500},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := parseSize(tc.in)
			if got != tc.want {
				t.Errorf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestReadFileTool
// ---------------------------------------------------------------------------

func TestReadFileTool(t *testing.T) {
	t.Run("success read of temp file", func(t *testing.T) {
		dir := t.TempDir()
		fPath := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(fPath, []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := NewReadFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "1MB"})
		result, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"test.txt"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if result.Content != "hello" {
			t.Errorf("got %q, want %q", result.Content, "hello")
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		dir := t.TempDir()
		rt := NewReadFileTool(config.FileToolConfig{BasePath: dir})
		result, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"../../etc/passwd"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true")
		}
		if !strings.Contains(result.Content, "path traversal") {
			t.Errorf("content %q does not mention path traversal", result.Content)
		}
	})

	t.Run("file not found returns IsError", func(t *testing.T) {
		dir := t.TempDir()
		rt := NewReadFileTool(config.FileToolConfig{BasePath: dir})
		result, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"nonexistent.txt"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true")
		}
		if !strings.Contains(result.Content, "failed to stat file") {
			t.Errorf("content %q does not contain 'failed to stat file'", result.Content)
		}
	})

	t.Run("file exceeding MaxFileSize returns IsError", func(t *testing.T) {
		dir := t.TempDir()
		// write 2049 bytes
		data := bytes.Repeat([]byte("x"), 2049)
		fPath := filepath.Join(dir, "big.txt")
		if err := os.WriteFile(fPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		rt := NewReadFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "2KB"})
		result, err := rt.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true")
		}
		if !strings.Contains(result.Content, "exceeds maximum allowed") {
			t.Errorf("content %q does not mention size limit", result.Content)
		}
	})

	t.Run("invalid JSON params returns Go error", func(t *testing.T) {
		dir := t.TempDir()
		rt := NewReadFileTool(config.FileToolConfig{BasePath: dir})
		_, err := rt.Execute(context.Background(), json.RawMessage(`{bad}`))
		if err == nil {
			t.Error("expected Go error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// TestWriteFileTool
// ---------------------------------------------------------------------------

func TestWriteFileTool(t *testing.T) {
	t.Run("success write and verify", func(t *testing.T) {
		dir := t.TempDir()
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "1MB"})
		result, err := wt.Execute(context.Background(), json.RawMessage(`{"path":"out.txt","content":"world"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
		if err != nil {
			t.Fatalf("file not found after write: %v", err)
		}
		if string(data) != "world" {
			t.Errorf("file contents %q, want %q", data, "world")
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		dir := t.TempDir()
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "1MB"})
		result, err := wt.Execute(context.Background(), json.RawMessage(`{"path":"nested/deep/file.txt","content":"x"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if _, err := os.Stat(filepath.Join(dir, "nested/deep/file.txt")); err != nil {
			t.Errorf("file not created: %v", err)
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		dir := t.TempDir()
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir})
		result, err := wt.Execute(context.Background(), json.RawMessage(`{"path":"../../etc/cron.d/evil","content":"x"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for path traversal")
		}
	})

	t.Run("content exceeding MaxFileSize returns IsError", func(t *testing.T) {
		dir := t.TempDir()
		// 1025 bytes > 1KB
		content := strings.Repeat("x", 1025)
		params := fmt.Sprintf(`{"path":"f.txt","content":%q}`, content)
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "1KB"})
		result, err := wt.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true")
		}
		if !strings.Contains(result.Content, "exceeds maximum allowed") {
			t.Errorf("content %q does not mention size limit", result.Content)
		}
	})

	t.Run("invalid JSON params returns Go error", func(t *testing.T) {
		dir := t.TempDir()
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir})
		_, err := wt.Execute(context.Background(), json.RawMessage(`{bad}`))
		if err == nil {
			t.Error("expected Go error, got nil")
		}
	})

	t.Run("write fails when target is a directory returns IsError", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("skipping: running as root")
		}
		dir := t.TempDir()
		// Create a directory where we want to write a file — WriteFile should fail
		targetDir := filepath.Join(dir, "isdir")
		if err := os.Mkdir(targetDir, 0o755); err != nil {
			t.Fatal(err)
		}
		wt := NewWriteFileTool(config.FileToolConfig{BasePath: dir, MaxFileSize: "1MB"})
		result, err := wt.Execute(context.Background(), json.RawMessage(`{"path":"isdir","content":"x"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true when writing to directory")
		}
	})
}

// ---------------------------------------------------------------------------
// TestListFilesTool
// ---------------------------------------------------------------------------

func TestListFilesTool(t *testing.T) {
	t.Run("success list with files and subdirs", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "mydir"), 0o755); err != nil {
			t.Fatal(err)
		}
		lt := NewListFilesTool(config.FileToolConfig{BasePath: dir})
		result, err := lt.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if !strings.Contains(result.Content, "file.txt") {
			t.Errorf("content %q missing 'file.txt'", result.Content)
		}
		if !strings.Contains(result.Content, "mydir/") {
			t.Errorf("content %q missing 'mydir/'", result.Content)
		}
	})

	t.Run("empty directory returns empty directory message", func(t *testing.T) {
		dir := t.TempDir()
		lt := NewListFilesTool(config.FileToolConfig{BasePath: dir})
		result, err := lt.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if result.Content != "(empty directory)" {
			t.Errorf("got %q, want '(empty directory)'", result.Content)
		}
	})

	t.Run("null params defaults to base path root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		lt := NewListFilesTool(config.FileToolConfig{BasePath: dir})
		result, err := lt.Execute(context.Background(), json.RawMessage("null"))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if !strings.Contains(result.Content, "readme.txt") {
			t.Errorf("content %q missing 'readme.txt'", result.Content)
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		dir := t.TempDir()
		lt := NewListFilesTool(config.FileToolConfig{BasePath: dir})
		result, err := lt.Execute(context.Background(), json.RawMessage(`{"path":"../../"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for path traversal")
		}
	})

	t.Run("non-existent directory returns IsError", func(t *testing.T) {
		dir := t.TempDir()
		lt := NewListFilesTool(config.FileToolConfig{BasePath: dir})
		result, err := lt.Execute(context.Background(), json.RawMessage(`{"path":"no_such_dir"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true")
		}
		if !strings.Contains(result.Content, "failed to list directory") {
			t.Errorf("content %q does not contain 'failed to list directory'", result.Content)
		}
	})
}

// ---------------------------------------------------------------------------
// TestHTTPFetchTool_Success
// ---------------------------------------------------------------------------

func TestHTTPFetchTool_Success(t *testing.T) {
	t.Run("GET request returns body", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "ok")
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		params := fmt.Sprintf(`{"url":%q}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if !strings.HasPrefix(result.Content, "Status: 200") {
			t.Errorf("content %q does not start with 'Status: 200'", result.Content)
		}
		if !strings.Contains(result.Content, "ok") {
			t.Errorf("content %q does not contain 'ok'", result.Content)
		}
	})

	t.Run("POST request with body and headers forwarded correctly", func(t *testing.T) {
		var gotMethod, gotBody, gotHeader string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(r.Body); err != nil {
				http.Error(w, "read body error", http.StatusInternalServerError)
				return
			}
			gotBody = buf.String()
			gotHeader = r.Header.Get("X-Custom")
			fmt.Fprint(w, "received")
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		params := fmt.Sprintf(`{"url":%q,"method":"post","body":"test=1","headers":{"X-Custom":"val"}}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if gotMethod != "POST" {
			t.Errorf("server got method %q, want POST", gotMethod)
		}
		if gotBody != "test=1" {
			t.Errorf("server got body %q, want 'test=1'", gotBody)
		}
		if gotHeader != "val" {
			t.Errorf("server got X-Custom=%q, want 'val'", gotHeader)
		}
	})

	t.Run("method is uppercased from lowercase get", func(t *testing.T) {
		var gotMethod string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			fmt.Fprint(w, "ok")
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		params := fmt.Sprintf(`{"url":%q,"method":"get"}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if gotMethod != "GET" {
			t.Errorf("server got method %q, want GET", gotMethod)
		}
	})
}

// ---------------------------------------------------------------------------
// TestHTTPFetchTool_ContextTimeout
// ---------------------------------------------------------------------------

func TestHTTPFetchTool_ContextTimeout(t *testing.T) {
	// Server that hangs indefinitely
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer ts.Close()
	defer close(block)

	ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	params := fmt.Sprintf(`{"url":%q}`, ts.URL)
	result, err := ht.Execute(ctx, json.RawMessage(params))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for timeout")
	}
	if result.Content != "HTTP request timed out" {
		t.Errorf("got %q, want 'HTTP request timed out'", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestHTTPFetchTool_Truncation
// ---------------------------------------------------------------------------

func TestHTTPFetchTool_Truncation(t *testing.T) {
	// Server returning exactly 10KB body
	body := strings.Repeat("x", 10*1024)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	ht := NewHTTPFetchTool(config.HTTPToolConfig{
		Timeout:         5 * time.Second,
		MaxResponseSize: "1KB",
	})
	params := fmt.Sprintf(`{"url":%q}`, ts.URL)
	result, err := ht.Execute(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected IsError=true: %q", result.Content)
	}
	if !strings.Contains(result.Content, "...(response truncated —") {
		t.Errorf("content does not contain truncation marker, got tail: %q", result.Content[max(0, len(result.Content)-120):])
	}
}

// ---------------------------------------------------------------------------
// TestHTTPFetchTool_BlockedDomains
// ---------------------------------------------------------------------------

func TestHTTPFetchTool_BlockedDomains(t *testing.T) {
	ht := NewHTTPFetchTool(config.HTTPToolConfig{
		Timeout:        5 * time.Second,
		BlockedDomains: []string{"evil.com"},
	})

	t.Run("direct blocked domain returns IsError", func(t *testing.T) {
		result, err := ht.Execute(context.Background(), json.RawMessage(`{"url":"http://evil.com/path"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for blocked domain")
		}
		if !strings.Contains(result.Content, "blocked") {
			t.Errorf("content %q does not contain 'blocked'", result.Content)
		}
	})

	t.Run("subdomain of blocked domain is also rejected", func(t *testing.T) {
		result, err := ht.Execute(context.Background(), json.RawMessage(`{"url":"http://sub.evil.com/path"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for blocked subdomain")
		}
		if !strings.Contains(result.Content, "blocked") {
			t.Errorf("content %q does not contain 'blocked'", result.Content)
		}
	})
}

// ---------------------------------------------------------------------------
// TestHTTPFetchTool_ErrorCases
// ---------------------------------------------------------------------------

func TestHTTPFetchTool_ErrorCases(t *testing.T) {
	t.Run("invalid URL returns IsError", func(t *testing.T) {
		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		result, err := ht.Execute(context.Background(), json.RawMessage(`{"url":"not a url ://"}`))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for invalid URL, got content: %q", result.Content)
		}
	})

	t.Run("HTTP 404 is NOT an error returns content with status", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(404)
			fmt.Fprint(w, "not found")
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		params := fmt.Sprintf(`{"url":%q}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true for 404 (should not be error), content: %q", result.Content)
		}
		if !strings.Contains(result.Content, "404") {
			t.Errorf("content %q does not contain '404'", result.Content)
		}
	})

	t.Run("network error returns IsError", func(t *testing.T) {
		// Server that immediately closes the connection
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Hijack the connection and close it immediately
			hj, ok := w.(http.Hijacker)
			if !ok {
				// Fallback: just write nothing and close
				w.WriteHeader(200)
				return
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		params := fmt.Sprintf(`{"url":%q}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if !result.IsError {
			t.Errorf("expected IsError=true for network error, content: %q", result.Content)
		}
	})

	t.Run("empty MaxResponseSize defaults to 2MB truncation", func(t *testing.T) {
		// Serve more than 2MB so the default cap triggers.
		largeBody := strings.Repeat("y", 2*1024*1024+1024)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, largeBody)
		}))
		defer ts.Close()

		ht := NewHTTPFetchTool(config.HTTPToolConfig{
			Timeout:         5 * time.Second,
			MaxResponseSize: "", // empty → defaults to 2MB inside Execute
		})
		params := fmt.Sprintf(`{"url":%q}`, ts.URL)
		result, err := ht.Execute(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
		if result.IsError {
			t.Errorf("unexpected IsError=true: %q", result.Content)
		}
		if !strings.Contains(result.Content, "...(response truncated —") {
			t.Errorf("expected truncation marker, content length %d", len(result.Content))
		}
	})

	t.Run("invalid JSON params returns Go error", func(t *testing.T) {
		ht := NewHTTPFetchTool(config.HTTPToolConfig{Timeout: 5 * time.Second})
		_, err := ht.Execute(context.Background(), json.RawMessage(`{bad}`))
		if err == nil {
			t.Error("expected Go error, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildRegistry_MCPDisabled
// ---------------------------------------------------------------------------

func TestBuildRegistry_MCPDisabled(t *testing.T) {
	t.Run("MCP disabled registry contains only built-ins", func(t *testing.T) {
		cfg := config.ToolsConfig{
			Shell: config.ShellToolConfig{Enabled: true},
			File:  config.FileToolConfig{Enabled: true},
			MCP:   config.MCPConfig{Enabled: false},
		}
		reg := BuildRegistrySimple(cfg)
		// Expect 4 built-in tools: shell_exec, read_file, write_file, list_files
		if len(reg) != 4 {
			t.Errorf("expected 4 tools with MCP disabled, got %d", len(reg))
		}
	})
}

// ---------------------------------------------------------------------------
// TestMergeTools
// ---------------------------------------------------------------------------

func TestMergeTools(t *testing.T) {
	t.Run("external tools merged into registry", func(t *testing.T) {
		reg := map[string]Tool{
			"built_in": NewShellTool(config.ShellToolConfig{}),
		}
		external := map[string]Tool{
			"mcp_tool": NewHTTPFetchTool(config.HTTPToolConfig{}),
		}
		MergeTools(reg, external)
		if _, ok := reg["mcp_tool"]; !ok {
			t.Error("expected 'mcp_tool' in registry after merge")
		}
		if len(reg) != 2 {
			t.Errorf("expected 2 tools, got %d", len(reg))
		}
	})

	t.Run("built-in wins on collision", func(t *testing.T) {
		builtIn := NewShellTool(config.ShellToolConfig{})
		reg := map[string]Tool{
			"shell_exec": builtIn,
		}
		// Provide an external tool with the same name
		external := map[string]Tool{
			"shell_exec": NewHTTPFetchTool(config.HTTPToolConfig{}),
		}
		MergeTools(reg, external)
		// The built-in must still be in the registry (no overwrite)
		if reg["shell_exec"] != builtIn {
			t.Error("expected built-in to win on collision")
		}
		if len(reg) != 1 {
			t.Errorf("expected 1 tool, got %d", len(reg))
		}
	})
}
