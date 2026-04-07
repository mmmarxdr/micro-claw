package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_ValidYAML(t *testing.T) {
	yamlData := `
agent:
  name: "TestAgent"
  personality: "Test Personality"
  max_iterations: 5
  max_tokens_per_turn: 1024
  history_length: 10
  memory_results: 3
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
  base_url: "http://test.com"
  timeout: 30s
  max_retries: 1
channel:
  type: "test_channel"
  token: "test-token"
  allowed_users: [12345, 67890]
tools:
  shell:
    enabled: true
    allowed_commands: ["echo"]
    allow_all: false
    working_dir: "/tmp"
  file:
    enabled: true
    base_path: "/data"
    max_file_size: "2MB"
  http:
    enabled: true
    timeout: 10s
    max_response_size: "1MB"
    blocked_domains: ["evil.com"]
store:
  type: "file"
  path: "/store"
logging:
  level: "debug"
  format: "json"
  file: "log.txt"
limits:
  tool_timeout: 15s
  total_timeout: 60s
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	if cfg.Agent.Name != "TestAgent" {
		t.Errorf("Expected Agent.Name 'TestAgent', got %q", cfg.Agent.Name)
	}
	if cfg.Provider.Timeout != 30*time.Second {
		t.Errorf("Expected Provider.Timeout 30s, got %v", cfg.Provider.Timeout)
	}
	if len(cfg.Channel.AllowedUsers) != 2 {
		t.Errorf("Expected 2 allowed users, got %d", len(cfg.Channel.AllowedUsers))
	}
}

func TestLoadConfig_AbsentMaxIterationsDefaults(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		wantMaxIter int
		wantErr     bool
	}{
		{
			name: "absent max_iterations defaults to 10",
			yaml: `
provider:
  api_key: "test-key"
`,
			wantMaxIter: 10,
			wantErr:     false,
		},
		{
			name: "explicit zero max_iterations defaults to 10",
			yaml: `
provider:
  api_key: "test-key"
agent:
  max_iterations: 0
`,
			wantMaxIter: 10,
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpFile := createTempFile(t, tc.yaml)
			defer os.Remove(tmpFile)

			cfg, err := Load(tmpFile)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load expected no error, got: %v", err)
			}
			if cfg.Agent.MaxIterations != tc.wantMaxIter {
				t.Errorf("expected MaxIterations=%d, got %d", tc.wantMaxIter, cfg.Agent.MaxIterations)
			}
		})
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	yamlData := `
provider:
  api_key: "test-key"
agent:
  max_iterations: 10
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	if cfg.Agent.MaxIterations != 10 {
		t.Errorf("Expected Agent.MaxIterations default 10, got %d", cfg.Agent.MaxIterations)
	}
	if cfg.Agent.HistoryLength != 20 {
		t.Errorf("Expected Agent.HistoryLength default 20, got %d", cfg.Agent.HistoryLength)
	}
	if cfg.Agent.MemoryResults != 5 {
		t.Errorf("Expected Agent.MemoryResults default 5, got %d", cfg.Agent.MemoryResults)
	}
	if cfg.Agent.MaxTokensPerTurn != 4096 {
		t.Errorf("Expected Agent.MaxTokensPerTurn default 4096, got %d", cfg.Agent.MaxTokensPerTurn)
	}
	if cfg.Provider.Timeout != 60*time.Second {
		t.Errorf("Expected Provider.Timeout default 60s, got %v", cfg.Provider.Timeout)
	}
	if cfg.Provider.MaxRetries != 3 {
		t.Errorf("Expected Provider.MaxRetries default 3, got %d", cfg.Provider.MaxRetries)
	}
	if cfg.Tools.Shell.AllowAll != false {
		t.Errorf("Expected Tools.Shell.AllowAll default false, got %t", cfg.Tools.Shell.AllowAll)
	}
	if cfg.Tools.File.MaxFileSize != "1MB" {
		t.Errorf("Expected Tools.File.MaxFileSize default 1MB, got %q", cfg.Tools.File.MaxFileSize)
	}
	if cfg.Tools.HTTP.Timeout != 15*time.Second {
		t.Errorf("Expected Tools.HTTP.Timeout default 15s, got %v", cfg.Tools.HTTP.Timeout)
	}
	if cfg.Limits.ToolTimeout != 30*time.Second {
		t.Errorf("Expected Limits.ToolTimeout default 30s, got %v", cfg.Limits.ToolTimeout)
	}
	if cfg.Limits.TotalTimeout != 120*time.Second {
		t.Errorf("Expected Limits.TotalTimeout default 120s, got %v", cfg.Limits.TotalTimeout)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("Expected Logging.Level default info, got %q", cfg.Logging.Level)
	}
	if cfg.Store.Type != "file" {
		t.Errorf("Expected Store.Type default file, got %q", cfg.Store.Type)
	}
}

func TestLoadConfig_EnvVarResolution(t *testing.T) {
	t.Setenv("TEST_API_KEY", "secret-from-env")

	tests := []struct {
		name     string
		yamlData string
		wantErr  bool
		checkAPI string
	}{
		{
			name: "resolves env var",
			yamlData: `
provider:
  type: "test"
  api_key: "${TEST_API_KEY}"
agent:
  max_iterations: 5
`,
			wantErr:  false,
			checkAPI: "secret-from-env",
		},
		{
			name: "undefined env var fails validation",
			yamlData: `
provider:
  type: "test"
  api_key: "${UNDEFINED_TEST_VAR}"
`,
			wantErr: true,
		},
		{
			name: "string literal maintained",
			yamlData: `
provider:
  type: "test"
  api_key: "direct-key"
agent:
  max_iterations: 5
`,
			wantErr:  false,
			checkAPI: "direct-key",
		},
		{
			name: "broken syntax literal",
			yamlData: `
provider:
  type: "test"
  api_key: "${PARTIAL"
agent:
  max_iterations: 5
`,
			wantErr:  false,
			checkAPI: "${PARTIAL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpFile := createTempFile(t, tc.yamlData)
			defer os.Remove(tmpFile)

			cfg, err := Load(tmpFile)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Expected error for %q, got nil", tc.name)
				}
			} else {
				if err != nil {
					t.Fatalf("Expected no error for %q, got: %v", tc.name, err)
				}
				if cfg.Provider.APIKey != tc.checkAPI {
					t.Errorf("Expected APIKey %q, got %q", tc.checkAPI, cfg.Provider.APIKey)
				}
			}
		})
	}
}

func TestLoadConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name     string
		yamlData string
	}{
		{
			name: "empty api key",
			yamlData: `
provider:
  type: anthropic
  api_key: ""
`,
		},
		{
			name: "unknown provider type",
			yamlData: `
provider:
  api_key: "abc"
  type: "quantum_brain"
`,
		},
		{
			name: "unknown channel type",
			yamlData: `
provider:
  api_key: "abc"
channel:
  type: "telepathy"
`,
		},
		{
			name: "negative max iterations",
			yamlData: `
provider:
  api_key: "abc"
agent:
  max_iterations: -1
`,
		},
		{
			name: "tool timeout exceeds total",
			yamlData: `
provider:
  api_key: "abc"
limits:
  tool_timeout: 100s
  total_timeout: 50s
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpFile := createTempFile(t, tc.yamlData)
			defer os.Remove(tmpFile)

			_, err := Load(tmpFile)
			if err == nil {
				t.Errorf("Expected validation error for %q, got nil", tc.name)
			}
		})
	}
}

func TestLoadConfig_TildeExpansion(t *testing.T) {
	homeDir, _ := os.UserHomeDir()

	yamlData := `
provider:
  api_key: "abc"
agent:
  max_iterations: 5
store:
  path: "~/.microagent/data"
tools:
  file:
    base_path: "~/workspace"
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	expectedStorePath := filepath.Join(homeDir, ".microagent/data")
	if cfg.Store.Path != expectedStorePath {
		t.Errorf("Expected Store.Path %q, got %q", expectedStorePath, cfg.Store.Path)
	}

	expectedBasePath := filepath.Join(homeDir, "workspace")
	if cfg.Tools.File.BasePath != expectedBasePath {
		t.Errorf("Expected Tools.File.BasePath %q, got %q", expectedBasePath, cfg.Tools.File.BasePath)
	}

	// Verify path without tilde doesn't expand
	yamlDataNoTilde := `
provider:
  api_key: "abc"
agent:
  max_iterations: 5
store:
  path: "/absolute/path"
`
	tmpFile2 := createTempFile(t, yamlDataNoTilde)
	defer os.Remove(tmpFile2)

	cfg2, err := Load(tmpFile2)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if cfg2.Store.Path != "/absolute/path" {
		t.Errorf("Expected Store.Path '/absolute/path', got %q", cfg2.Store.Path)
	}
}

func TestLoadConfig_FilePriority(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test configs
	flagConfig := filepath.Join(tmpDir, "flag.yaml")
	if err := os.WriteFile(flagConfig, []byte("provider:\n  api_key: \"flag\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write flag config: %v", err)
	}

	localConfig := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(localConfig, []byte("provider:\n  api_key: \"local\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	homeConfigDir := filepath.Join(tmpDir, ".microagent")
	if err := os.MkdirAll(homeConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir home config dir: %v", err)
	}
	homeConfig := filepath.Join(homeConfigDir, "config.yaml")
	if err := os.WriteFile(homeConfig, []byte("provider:\n  api_key: \"home\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	// Mock os.UserHomeDir temporarily by overriding the internal resolver var (we'll implement this hook in config.go later if needed, or just test logic)
	// For testing FilePriority logic per se, LoadAuto reads the rules.

	// Rule 1: Flag passed
	cfg, err := Load(flagConfig)
	if err != nil || cfg.Provider.APIKey != "flag" {
		t.Errorf("Expected to load flag config, got %v (err: %v)", cfg.Provider.APIKey, err)
	}

	// Rule 2 & 3: Find default paths. We will add a ResolvePath logic to test these cleanly in unit tests
}

func TestFindConfigPath_Override(t *testing.T) {
	t.Run("override path exists returns path", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "my-config.yaml")
		if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		got, err := FindConfigPath(cfgPath)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if got != cfgPath {
			t.Errorf("expected %q, got %q", cfgPath, got)
		}
	})

	t.Run("override path does not exist returns error", func(t *testing.T) {
		tmpDir := t.TempDir()
		nonexistent := filepath.Join(tmpDir, "nonexistent.yaml")
		_, err := FindConfigPath(nonexistent)
		if err == nil {
			t.Errorf("expected error for non-existent override path, got nil")
		}
	})
}

func TestFindConfigPath_NoOverride(t *testing.T) {
	t.Run("empty override exercises fallback logic without error if no files exist", func(t *testing.T) {
		// FindConfigPath("") tries ~/.microagent/config.yaml then ./config.yaml
		// Neither likely exists in a clean test environment; it should return an error about "no config file found"
		// We can't guarantee ./config.yaml doesn't exist in the working dir, so just verify the function doesn't panic
		// and returns either a valid path or an error.
		path, err := FindConfigPath("")
		if err != nil {
			// Expected when no config files are present — acceptable
			if !strings.Contains(err.Error(), "no config file found") {
				t.Errorf("expected 'no config file found' error, got: %v", err)
			}
		} else {
			// If a path is returned, it must be non-empty
			if path == "" {
				t.Errorf("expected non-empty path when no error returned")
			}
		}
	})
}

func TestLoad_InvalidYAML(t *testing.T) {
	invalidYAML := "not: valid: yaml: [unclosed"
	tmpFile := createTempFile(t, invalidYAML)
	defer os.Remove(tmpFile)

	_, err := Load(tmpFile)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
	if !strings.Contains(err.Error(), "parsing") && !strings.Contains(err.Error(), "yaml") {
		t.Errorf("expected error to mention 'parsing' or 'yaml', got: %v", err)
	}
}

func TestLoad_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test: running as root")
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "unreadable.yaml")
	if err := os.WriteFile(cfgPath, []byte("provider:\n  api_key: abc\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, err := Load(cfgPath)
	if err == nil {
		t.Fatal("expected error for unreadable file, got nil")
	}
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("expected error to mention 'reading', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestMCPServerConfigValidate
// ---------------------------------------------------------------------------

func TestMCPServerConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     MCPServerConfig
		wantErr bool
		errStr  string
	}{
		{
			name:    "valid stdio with command",
			cfg:     MCPServerConfig{Name: "fs", Transport: "stdio", Command: []string{"npx", "arg"}},
			wantErr: false,
		},
		{
			name:    "valid http with url",
			cfg:     MCPServerConfig{Name: "remote", Transport: "http", URL: "http://localhost:3000/mcp"},
			wantErr: false,
		},
		{
			name:    "stdio missing command",
			cfg:     MCPServerConfig{Name: "fs", Transport: "stdio", Command: []string{}},
			wantErr: true,
			errStr:  "command",
		},
		{
			name:    "http missing url",
			cfg:     MCPServerConfig{Name: "remote", Transport: "http", URL: ""},
			wantErr: true,
			errStr:  "url",
		},
		{
			name:    "unknown transport",
			cfg:     MCPServerConfig{Name: "bad", Transport: "grpc"},
			wantErr: true,
			errStr:  "grpc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tc.errStr != "" && !strings.Contains(err.Error(), tc.errStr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errStr)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestMCPDefaults verifies that applyDefaults sets ConnectTimeout to 10s.
func TestMCPDefaults(t *testing.T) {
	yamlData := `
provider:
  api_key: "test-key"
tools:
  mcp:
    enabled: true
    servers:
      - name: fs
        transport: stdio
        command: ["echo", "hello"]
`
	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	const wantTimeout = 10 * time.Second
	if cfg.Tools.MCP.ConnectTimeout != wantTimeout {
		t.Errorf("MCP.ConnectTimeout = %v, want %v", cfg.Tools.MCP.ConnectTimeout, wantTimeout)
	}
}

// TestMCPValidation verifies that Config.validate() propagates per-server validation.
func TestMCPValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		errStr  string
	}{
		{
			name: "valid mcp config passes validation",
			yaml: `
provider:
  api_key: "test-key"
tools:
  mcp:
    enabled: true
    servers:
      - name: fs
        transport: stdio
        command: ["npx", "mcp-server"]
`,
			wantErr: false,
		},
		{
			name: "invalid server config propagates error",
			yaml: `
provider:
  api_key: "test-key"
tools:
  mcp:
    enabled: true
    servers:
      - name: bad-server
        transport: stdio
`,
			wantErr: true,
			errStr:  "command",
		},
		{
			name: "unknown transport fails",
			yaml: `
provider:
  api_key: "test-key"
tools:
  mcp:
    enabled: true
    servers:
      - name: bad-server
        transport: grpc
`,
			wantErr: true,
			errStr:  "grpc",
		},
		{
			name: "mcp disabled skips validation",
			yaml: `
provider:
  api_key: "test-key"
tools:
  mcp:
    enabled: false
    servers:
      - name: bad-server
        transport: stdio
`,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpFile := createTempFile(t, tc.yaml)
			defer os.Remove(tmpFile)

			_, err := Load(tmpFile)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tc.errStr != "" && !strings.Contains(err.Error(), tc.errStr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errStr)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestConfig_StoreType_Valid verifies that known store types pass validation.
func TestConfig_StoreType_Valid(t *testing.T) {
	for _, storeType := range []string{"file", "sqlite", ""} {
		t.Run("type="+storeType, func(t *testing.T) {
			yaml := `
provider:
  api_key: "test-key"
store:
  type: "` + storeType + `"
`
			tmpFile := createTempFile(t, yaml)
			defer os.Remove(tmpFile)

			_, err := Load(tmpFile)
			if err != nil {
				t.Errorf("expected no error for store.type=%q, got: %v", storeType, err)
			}
		})
	}
}

// TestConfig_StoreType_Invalid verifies that unknown store types fail validation.
func TestConfig_StoreType_Invalid(t *testing.T) {
	yaml := `
provider:
  api_key: "test-key"
store:
  type: "badger"
`
	tmpFile := createTempFile(t, yaml)
	defer os.Remove(tmpFile)

	_, err := Load(tmpFile)
	if err == nil {
		t.Fatal("expected validation error for store.type='badger', got nil")
	}
	if !strings.Contains(err.Error(), "unknown store.type") {
		t.Errorf("error should contain 'unknown store.type', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Phase 6b: ErrNoConfig sentinel and applyDefaults path defaults
// ---------------------------------------------------------------------------

func TestApplyDefaults_StorePathDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Store.Path != "~/.microagent/data" {
		t.Errorf("Store.Path = %q, want %q", cfg.Store.Path, "~/.microagent/data")
	}
}

func TestApplyDefaults_AuditPathDefault(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Audit.Path != "~/.microagent/audit" {
		t.Errorf("Audit.Path = %q, want %q", cfg.Audit.Path, "~/.microagent/audit")
	}
}

func TestApplyDefaults_PreservesExplicitPaths(t *testing.T) {
	cfg := &Config{
		Store: StoreConfig{Path: "/custom/store"},
		Audit: AuditConfig{Path: "/custom/audit"},
	}
	cfg.applyDefaults()
	if cfg.Store.Path != "/custom/store" {
		t.Errorf("Store.Path = %q, want %q", cfg.Store.Path, "/custom/store")
	}
	if cfg.Audit.Path != "/custom/audit" {
		t.Errorf("Audit.Path = %q, want %q", cfg.Audit.Path, "/custom/audit")
	}
}

func TestErrNoConfig_ErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", ErrNoConfig)
	if !errors.Is(wrapped, ErrNoConfig) {
		t.Error("errors.Is(wrapped, ErrNoConfig) should be true for a wrapped ErrNoConfig")
	}
	other := fmt.Errorf("other error")
	if errors.Is(other, ErrNoConfig) {
		t.Error("errors.Is(other, ErrNoConfig) should be false for an unrelated error")
	}
}

// ---------------------------------------------------------------------------
// Phase 9 — Ollama api_key validation tests
// ---------------------------------------------------------------------------

func TestValidate_OllamaEmptyAPIKeyAllowed(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Type:  "ollama",
			Model: "llama3.2",
		},
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		t.Errorf("expected no validation error for ollama with empty api_key, got: %v", err)
	}
}

func TestValidate_AnthropicEmptyAPIKeyStillFails(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Type: "anthropic",
		},
	}
	cfg.applyDefaults()
	err := cfg.validate()
	if err == nil {
		t.Error("expected validation error for anthropic with empty api_key, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("expected error to contain 'api_key is required', got: %v", err)
	}
}

func TestValidate_OllamaOtherChecksStillActive(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{
			Type: "ollama",
		},
		Agent: AgentConfig{
			MaxIterations: -1, // invalid — should still be caught
		},
	}
	cfg.applyDefaults()
	// Override the default MaxIterations to the invalid value
	cfg.Agent.MaxIterations = -1
	err := cfg.validate()
	if err == nil {
		t.Error("expected validation error for negative MaxIterations, got nil")
	}
	if err != nil && strings.Contains(err.Error(), "api_key is required") {
		t.Errorf("error should be about max_iterations, not api_key: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FIX 1 — Empty / whitespace-only config file returns ErrNoConfig
// ---------------------------------------------------------------------------

func TestLoad_EmptyFileReturnsErrNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(cfgPath)
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("expected errors.Is(err, ErrNoConfig) to be true for empty file, got: %v", err)
	}
}

func TestLoad_WhitespaceOnlyFileReturnsErrNoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("\n\n  \n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(cfgPath)
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("expected errors.Is(err, ErrNoConfig) to be true for whitespace-only file, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Task 1.4 — FilterConfig defaults
// ---------------------------------------------------------------------------

func TestApplyDefaults_FilterConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.Filter.TruncationChars != 8000 {
		t.Errorf("Filter.TruncationChars = %d, want 8000", cfg.Filter.TruncationChars)
	}
	if cfg.Filter.Levels.Shell != "aggressive" {
		t.Errorf("Filter.Levels.Shell = %q, want %q", cfg.Filter.Levels.Shell, "aggressive")
	}
	if cfg.Filter.Levels.FileRead != "minimal" {
		t.Errorf("Filter.Levels.FileRead = %q, want %q", cfg.Filter.Levels.FileRead, "minimal")
	}
	// When disabled (default), Generic remains false.
	if cfg.Filter.Levels.Generic {
		t.Errorf("Filter.Levels.Generic should be false when filter is disabled, got true")
	}
}

func TestApplyDefaults_FilterConfig_EnabledSetsGenericTrue(t *testing.T) {
	cfg := &Config{
		Filter: FilterConfig{Enabled: true},
	}
	cfg.applyDefaults()

	if !cfg.Filter.Levels.Generic {
		t.Errorf("Filter.Levels.Generic should be true when filter is enabled, got false")
	}
}

// ─── Task 1.5 — New config fields for native-memory ──────────────────────────

func TestApplyDefaults_NativeMemoryDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	// Enrichment defaults.
	if cfg.Agent.EnrichMemory != false {
		t.Errorf("Agent.EnrichMemory default should be false, got %v", cfg.Agent.EnrichMemory)
	}
	if cfg.Agent.EnrichRatePerMin != 10 {
		t.Errorf("Agent.EnrichRatePerMin default should be 10, got %d", cfg.Agent.EnrichRatePerMin)
	}

	// Pruning defaults.
	const wantPruneInterval = 24 * time.Hour
	if cfg.Agent.PruneInterval != wantPruneInterval {
		t.Errorf("Agent.PruneInterval default should be 24h, got %v", cfg.Agent.PruneInterval)
	}
	if cfg.Agent.PruneRetentionDays != 30 {
		t.Errorf("Agent.PruneRetentionDays default should be 30, got %d", cfg.Agent.PruneRetentionDays)
	}
	if cfg.Agent.PruneThreshold != 0.1 {
		t.Errorf("Agent.PruneThreshold default should be 0.1, got %f", cfg.Agent.PruneThreshold)
	}

	// Embeddings default.
	if cfg.Store.Embeddings != false {
		t.Errorf("Store.Embeddings default should be false, got %v", cfg.Store.Embeddings)
	}
}

func TestValidate_NativeMemory_EnrichRatePerMinInvalid(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent: AgentConfig{
			MaxIterations:    10,
			EnrichMemory:     true,
			EnrichRatePerMin: -1, // invalid
		},
	}
	cfg.applyDefaults()
	cfg.Agent.EnrichRatePerMin = -1 // override applied default
	cfg.Agent.EnrichMemory = true

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected validation error for negative enrich_rate_per_minute, got nil")
	}
	if !strings.Contains(err.Error(), "enrich_rate_per_minute") {
		t.Errorf("error should mention 'enrich_rate_per_minute', got: %v", err)
	}
}

func TestValidate_NativeMemory_EmbeddingsRequiresSQLite(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent:    AgentConfig{MaxIterations: 10},
		Store: StoreConfig{
			Type:       "file",
			Embeddings: true,
		},
	}
	cfg.applyDefaults()

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected validation error when embeddings=true with non-sqlite store, got nil")
	}
	if !strings.Contains(err.Error(), "embeddings") {
		t.Errorf("error should mention 'embeddings', got: %v", err)
	}
}

func TestValidate_NativeMemory_EmbeddingsWithSQLiteAllowed(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent:    AgentConfig{MaxIterations: 10},
		Store: StoreConfig{
			Type:       "sqlite",
			Embeddings: true,
		},
	}
	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		t.Errorf("expected no error for embeddings=true with sqlite, got: %v", err)
	}
}

func TestValidate_NativeMemory_PruneIntervalZeroInvalid(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent: AgentConfig{
			MaxIterations: 10,
			PruneInterval: -1,
		},
	}
	cfg.applyDefaults()
	cfg.Agent.PruneInterval = -1 // override default

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected validation error for negative prune_interval, got nil")
	}
	if !strings.Contains(err.Error(), "prune_interval") {
		t.Errorf("error should mention 'prune_interval', got: %v", err)
	}
}

func TestValidate_NativeMemory_PruneRetentionDaysNegativeInvalid(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent: AgentConfig{
			MaxIterations:      10,
			PruneRetentionDays: -5,
		},
	}
	cfg.applyDefaults()
	cfg.Agent.PruneRetentionDays = -5 // override default

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected validation error for negative prune_retention_days, got nil")
	}
	if !strings.Contains(err.Error(), "prune_retention_days") {
		t.Errorf("error should mention 'prune_retention_days', got: %v", err)
	}
}

func TestValidate_NativeMemory_PruneThresholdOutOfRange(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
	}{
		{"negative threshold", -0.1},
		{"threshold > 1.0", 1.5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Provider: ProviderConfig{APIKey: "test-key"},
				Agent: AgentConfig{
					MaxIterations:  10,
					PruneThreshold: tc.threshold,
				},
			}
			cfg.applyDefaults()
			cfg.Agent.PruneThreshold = tc.threshold // override default

			err := cfg.validate()
			if err == nil {
				t.Fatalf("expected validation error for prune_threshold=%v, got nil", tc.threshold)
			}
			if !strings.Contains(err.Error(), "prune_threshold") {
				t.Errorf("error should mention 'prune_threshold', got: %v", err)
			}
		})
	}
}

func TestValidate_NativeMemory_ValidThreshold(t *testing.T) {
	cfg := &Config{
		Provider: ProviderConfig{APIKey: "test-key"},
		Agent: AgentConfig{
			MaxIterations:  10,
			PruneThreshold: 0.5,
		},
	}
	cfg.applyDefaults()
	cfg.Agent.PruneThreshold = 0.5

	if err := cfg.validate(); err != nil {
		t.Errorf("expected no error for prune_threshold=0.5, got: %v", err)
	}
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "microagent-config-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	if _, err := f.Write([]byte(strings.TrimSpace(content))); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	f.Close()
	return f.Name()
}
