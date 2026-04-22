package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	// After v1→v2 migration, Provider pointer is nil and credentials live in Providers map.
	if cfg.Provider != nil {
		t.Errorf("Expected Provider pointer to be nil after migration, got %+v", cfg.Provider)
	}
	if cfg.Models.Default.Provider != "test_provider" {
		t.Errorf("Expected Models.Default.Provider = 'test_provider', got %q", cfg.Models.Default.Provider)
	}
	if creds := cfg.Providers["test_provider"]; creds.APIKey != "test-key" {
		t.Errorf("Expected Providers[test_provider].APIKey = 'test-key', got %q", creds.APIKey)
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
			// Step 4a: absent max_iterations means "no hard cap" (0). Previously
			// this defaulted to 10; the philosophy shifted to "semi-autonomous
			// unless the user opts in to a cap".
			name: "absent max_iterations stays 0 (unlimited)",
			yaml: `
provider:
  api_key: "test-key"
`,
			wantMaxIter: 0,
			wantErr:     false,
		},
		{
			name: "explicit zero max_iterations stays 0 (unlimited)",
			yaml: `
provider:
  api_key: "test-key"
agent:
  max_iterations: 0
`,
			wantMaxIter: 0,
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
  type: "anthropic"
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
		t.Errorf("Expected Agent.MaxIterations=10 (explicitly set in YAML), got %d", cfg.Agent.MaxIterations)
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
	// Provider Timeout/MaxRetries defaults are now applied by ResolveActiveProvider.
	// Verify that after v1→v2 migration the Providers map is populated.
	if cfg.Provider != nil {
		t.Errorf("Expected Provider pointer nil after migration, got non-nil")
	}
	resolved := ResolveActiveProvider(*cfg)
	if resolved.Timeout != 60*time.Second {
		t.Errorf("Expected resolved Provider.Timeout default 60s, got %v", resolved.Timeout)
	}
	if resolved.MaxRetries != 3 {
		t.Errorf("Expected resolved Provider.MaxRetries default 3, got %d", resolved.MaxRetries)
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
				// After v1→v2 migration, api_key lives in Providers map.
				resolved := ResolveActiveProvider(*cfg)
				if resolved.APIKey != tc.checkAPI {
					t.Errorf("Expected APIKey %q, got %q (via ResolveActiveProvider)", tc.checkAPI, resolved.APIKey)
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
		{
			name: "rag embedding enabled without provider",
			yamlData: `
provider:
  api_key: "abc"
rag:
  enabled: true
  embedding:
    enabled: true
    api_key: "sk-x"
`,
		},
		{
			name: "rag embedding enabled with unsupported provider",
			yamlData: `
provider:
  api_key: "abc"
rag:
  enabled: true
  embedding:
    enabled: true
    provider: "openrouter"
    api_key: "sk-x"
`,
		},
		{
			name: "rag embedding enabled without api_key",
			yamlData: `
provider:
  api_key: "abc"
rag:
  enabled: true
  embedding:
    enabled: true
    provider: "openai"
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
  path: "~/.daimon/data"
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

	expectedStorePath := filepath.Join(homeDir, ".daimon/data")
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
	if err := os.WriteFile(flagConfig, []byte("provider:\n  type: anthropic\n  api_key: \"flag\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write flag config: %v", err)
	}

	localConfig := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(localConfig, []byte("provider:\n  type: anthropic\n  api_key: \"local\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	homeConfigDir := filepath.Join(tmpDir, ".daimon")
	if err := os.MkdirAll(homeConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir home config dir: %v", err)
	}
	homeConfig := filepath.Join(homeConfigDir, "config.yaml")
	if err := os.WriteFile(homeConfig, []byte("provider:\n  type: anthropic\n  api_key: \"home\"\nagent:\n  max_iterations: 5\n"), 0o644); err != nil {
		t.Fatalf("write home config: %v", err)
	}

	// Mock os.UserHomeDir temporarily by overriding the internal resolver var (we'll implement this hook in config.go later if needed, or just test logic)
	// For testing FilePriority logic per se, LoadAuto reads the rules.

	// Rule 1: Flag passed
	cfg, err := Load(flagConfig)
	if err != nil {
		t.Errorf("Expected to load flag config, got error: %v", err)
	} else {
		resolved := ResolveActiveProvider(*cfg)
		if resolved.APIKey != "flag" {
			t.Errorf("Expected APIKey 'flag', got %q (via ResolveActiveProvider)", resolved.APIKey)
		}
	}

	// Rule 2 & 3: Find default paths. We will add a ResolvePath logic to test these cleanly in unit tests
}

// TestLoad_V1ConfigMigratesInMemory — AS-1: v1 YAML migrated in-memory, file NOT written (T-04/T-05).
func TestLoad_V1ConfigMigratesInMemory(t *testing.T) {
	v1YAML := `
provider:
  type: openrouter
  model: anthropic/claude-haiku-4.5
  api_key: sk-or-abc
  base_url: ""
agent:
  max_iterations: 5
`
	tmpFile := createTempFile(t, v1YAML)
	defer os.Remove(tmpFile)

	originalStat, err := os.Stat(tmpFile)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	// After migration: Provider pointer must be nil.
	if cfg.Provider != nil {
		t.Error("expected cfg.Provider == nil after v1→v2 migration")
	}
	// Providers map populated correctly.
	creds, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatal("Providers[openrouter] not found after Load+migrate")
	}
	if creds.APIKey != "sk-or-abc" {
		t.Errorf("Providers[openrouter].APIKey = %q, want sk-or-abc", creds.APIKey)
	}
	// Models.Default populated.
	if cfg.Models.Default.Provider != "openrouter" {
		t.Errorf("Models.Default.Provider = %q, want openrouter", cfg.Models.Default.Provider)
	}
	if cfg.Models.Default.Model != "anthropic/claude-haiku-4.5" {
		t.Errorf("Models.Default.Model = %q, want anthropic/claude-haiku-4.5", cfg.Models.Default.Model)
	}

	// File on disk UNCHANGED (no write occurred).
	afterStat, err := os.Stat(tmpFile)
	if err != nil {
		t.Fatalf("stat after Load: %v", err)
	}
	if !afterStat.ModTime().Equal(originalStat.ModTime()) {
		t.Error("file modification time changed — Load must not write to disk")
	}
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
		// FindConfigPath("") tries ~/.daimon/config.yaml then ./config.yaml
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
	cfg.ApplyDefaults()
	if cfg.Store.Path != "~/.daimon/data" {
		t.Errorf("Store.Path = %q, want %q", cfg.Store.Path, "~/.daimon/data")
	}
}

func TestApplyDefaults_AuditPathDefault(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Audit.Path != "~/.daimon/audit" {
		t.Errorf("Audit.Path = %q, want %q", cfg.Audit.Path, "~/.daimon/audit")
	}
}

func TestApplyDefaults_PreservesExplicitPaths(t *testing.T) {
	cfg := &Config{
		Store: StoreConfig{Path: "/custom/store"},
		Audit: AuditConfig{Path: "/custom/audit"},
	}
	cfg.ApplyDefaults()
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
		Providers: map[string]ProviderCredentials{
			"ollama": {APIKey: ""},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "ollama", Model: "llama3.2"}},
	}
	cfg.ApplyDefaults()
	if err := cfg.validate(); err != nil {
		t.Errorf("expected no validation error for ollama with empty api_key, got: %v", err)
	}
}

func TestValidate_AnthropicEmptyAPIKeyStillFails(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderCredentials{
			"anthropic": {APIKey: ""},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
	}
	cfg.ApplyDefaults()
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
		Providers: map[string]ProviderCredentials{
			"ollama": {APIKey: ""},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "ollama", Model: "llama3.2"}},
		Agent: AgentConfig{
			MaxIterations: -1, // invalid — should still be caught
		},
	}
	cfg.ApplyDefaults()
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
	cfg.ApplyDefaults()

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
	cfg.ApplyDefaults()

	if !cfg.Filter.Levels.Generic {
		t.Errorf("Filter.Levels.Generic should be true when filter is enabled, got false")
	}
}

// ─── Task 1.5 — New config fields for native-memory ──────────────────────────

func TestApplyDefaults_NativeMemoryDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

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
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent: AgentConfig{
			MaxIterations:    10,
			EnrichMemory:     true,
			EnrichRatePerMin: -1, // invalid
		},
	}
	cfg.ApplyDefaults()
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
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent:     AgentConfig{MaxIterations: 10},
		Store: StoreConfig{
			Type:       "file",
			Embeddings: true,
		},
	}
	cfg.ApplyDefaults()

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
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent:     AgentConfig{MaxIterations: 10},
		Store: StoreConfig{
			Type:       "sqlite",
			Embeddings: true,
		},
	}
	cfg.ApplyDefaults()

	if err := cfg.validate(); err != nil {
		t.Errorf("expected no error for embeddings=true with sqlite, got: %v", err)
	}
}

func TestValidate_NativeMemory_PruneIntervalZeroInvalid(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent: AgentConfig{
			MaxIterations: 10,
			PruneInterval: -1,
		},
	}
	cfg.ApplyDefaults()
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
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent: AgentConfig{
			MaxIterations:      10,
			PruneRetentionDays: -5,
		},
	}
	cfg.ApplyDefaults()
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
				Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
				Agent: AgentConfig{
					MaxIterations:  10,
					PruneThreshold: tc.threshold,
				},
			}
			cfg.ApplyDefaults()
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
		Providers: map[string]ProviderCredentials{"anthropic": {APIKey: "test-key"}},
		Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}},
		Agent: AgentConfig{
			MaxIterations:  10,
			PruneThreshold: 0.5,
		},
	}
	cfg.ApplyDefaults()
	cfg.Agent.PruneThreshold = 0.5

	if err := cfg.validate(); err != nil {
		t.Errorf("expected no error for prune_threshold=0.5, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestContextConfig_ResolveMaxTokens
// ---------------------------------------------------------------------------

func TestContextConfig_ResolveMaxTokens(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens interface{}
		want      int
	}{
		{name: "int 200000", maxTokens: 200000, want: 200000},
		{name: "float64 200000.0", maxTokens: float64(200000.0), want: 200000},
		{name: "string auto", maxTokens: "auto", want: 0},
		{name: "nil", maxTokens: nil, want: 0},
		{name: "int 0", maxTokens: 0, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ContextConfig{MaxTokens: tc.maxTokens}
			got := c.ResolveMaxTokens()
			if got != tc.want {
				t.Errorf("ResolveMaxTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestContextConfig_ApplyContextDefaults
// ---------------------------------------------------------------------------

func TestContextConfig_ApplyContextDefaults(t *testing.T) {
	t.Run("fills all zero fields", func(t *testing.T) {
		c := ContextConfig{}
		c.ApplyContextDefaults()

		if c.CompactThreshold != 0.8 {
			t.Errorf("CompactThreshold = %v, want 0.8", c.CompactThreshold)
		}
		if c.CooldownTurns != 3 {
			t.Errorf("CooldownTurns = %d, want 3", c.CooldownTurns)
		}
		if c.SummaryMaxTokens != 1000 {
			t.Errorf("SummaryMaxTokens = %d, want 1000", c.SummaryMaxTokens)
		}
		if c.ProtectedTurns != 5 {
			t.Errorf("ProtectedTurns = %d, want 5", c.ProtectedTurns)
		}
		if c.ToolResultMaxChars != 800 {
			t.Errorf("ToolResultMaxChars = %d, want 800", c.ToolResultMaxChars)
		}
		if c.Strategy != "smart" {
			t.Errorf("Strategy = %q, want 'smart'", c.Strategy)
		}
		if c.Notify == nil || !*c.Notify {
			t.Errorf("Notify = %v, want pointer-to-true", c.Notify)
		}
		if c.FallbackCtxSize != 128000 {
			t.Errorf("FallbackCtxSize = %d, want 128000", c.FallbackCtxSize)
		}
	})

	t.Run("does not overwrite non-zero fields", func(t *testing.T) {
		notifyFalse := false
		c := ContextConfig{
			CompactThreshold:   0.5,
			CooldownTurns:      7,
			SummaryMaxTokens:   500,
			ProtectedTurns:     2,
			ToolResultMaxChars: 200,
			Strategy:           "legacy",
			Notify:             &notifyFalse,
			FallbackCtxSize:    64000,
		}
		c.ApplyContextDefaults()

		if c.CompactThreshold != 0.5 {
			t.Errorf("CompactThreshold overwritten: got %v, want 0.5", c.CompactThreshold)
		}
		if c.CooldownTurns != 7 {
			t.Errorf("CooldownTurns overwritten: got %d, want 7", c.CooldownTurns)
		}
		if c.SummaryMaxTokens != 500 {
			t.Errorf("SummaryMaxTokens overwritten: got %d, want 500", c.SummaryMaxTokens)
		}
		if c.ProtectedTurns != 2 {
			t.Errorf("ProtectedTurns overwritten: got %d, want 2", c.ProtectedTurns)
		}
		if c.ToolResultMaxChars != 200 {
			t.Errorf("ToolResultMaxChars overwritten: got %d, want 200", c.ToolResultMaxChars)
		}
		if c.Strategy != "legacy" {
			t.Errorf("Strategy overwritten: got %q, want 'legacy'", c.Strategy)
		}
		if c.Notify == nil || *c.Notify != false {
			t.Errorf("Notify overwritten: got %v, want pointer-to-false", c.Notify)
		}
		if c.FallbackCtxSize != 64000 {
			t.Errorf("FallbackCtxSize overwritten: got %d, want 64000", c.FallbackCtxSize)
		}
	})
}

// ---------------------------------------------------------------------------
// TestAgentConfig_HasContextField
// ---------------------------------------------------------------------------

func TestAgentConfig_HasContextField(t *testing.T) {
	// Verify that AgentConfig has a Context field of type ContextConfig.
	cfg := AgentConfig{}
	cfg.Context.ApplyContextDefaults()
	if cfg.Context.Strategy != "smart" {
		t.Errorf("AgentConfig.Context.Strategy = %q, want 'smart' after defaults", cfg.Context.Strategy)
	}
}

// ---------------------------------------------------------------------------
// T6.1 — ContextConfig YAML round-trip and validation edge cases
// ---------------------------------------------------------------------------

func TestContextConfig_YAMLRoundTrip_WithContextBlock(t *testing.T) {
	yamlData := `
provider:
  api_key: "test-key"
agent:
  max_iterations: 5
  context:
    max_tokens: 200000
    compact_threshold: 0.75
    cooldown_turns: 4
    summary_max_tokens: 1500
    protected_turns: 3
    tool_result_max_chars: 600
    strategy: "legacy"
    fallback_context_size: 64000
    summary_model: "claude-3-haiku-20240307"
`
	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	ctx := cfg.Agent.Context
	if v := ctx.ResolveMaxTokens(); v != 200000 {
		t.Errorf("MaxTokens: got %d, want 200000", v)
	}
	if ctx.CompactThreshold != 0.75 {
		t.Errorf("CompactThreshold: got %v, want 0.75", ctx.CompactThreshold)
	}
	if ctx.CooldownTurns != 4 {
		t.Errorf("CooldownTurns: got %d, want 4", ctx.CooldownTurns)
	}
	if ctx.SummaryMaxTokens != 1500 {
		t.Errorf("SummaryMaxTokens: got %d, want 1500", ctx.SummaryMaxTokens)
	}
	if ctx.ProtectedTurns != 3 {
		t.Errorf("ProtectedTurns: got %d, want 3", ctx.ProtectedTurns)
	}
	if ctx.ToolResultMaxChars != 600 {
		t.Errorf("ToolResultMaxChars: got %d, want 600", ctx.ToolResultMaxChars)
	}
	if ctx.Strategy != "legacy" {
		t.Errorf("Strategy: got %q, want 'legacy'", ctx.Strategy)
	}
	if ctx.FallbackCtxSize != 64000 {
		t.Errorf("FallbackCtxSize: got %d, want 64000", ctx.FallbackCtxSize)
	}
	if ctx.SummaryModel != "claude-3-haiku-20240307" {
		t.Errorf("SummaryModel: got %q, want 'claude-3-haiku-20240307'", ctx.SummaryModel)
	}
}

func TestContextConfig_YAMLRoundTrip_WithoutContextBlock_DefaultsApplied(t *testing.T) {
	// No agent.context block → all defaults should be applied by ApplyContextDefaults.
	// Note: ApplyContextDefaults is called by NewContextManager, not by Load.
	// Load only parses the YAML; the zero-value ContextConfig is stored.
	// We verify the zero-value here and rely on NewContextManager tests for defaults.
	yamlData := `
provider:
  api_key: "test-key"
agent:
  max_iterations: 5
`
	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	ctx := cfg.Agent.Context
	// Without a context block, fields are zero-value (defaults applied by ContextManager).
	if ctx.Strategy != "" {
		t.Errorf("Strategy without context block: got %q, want empty string (zero-value)", ctx.Strategy)
	}
	if ctx.CompactThreshold != 0 {
		t.Errorf("CompactThreshold without context block: got %v, want 0 (zero-value)", ctx.CompactThreshold)
	}
	// Verify ApplyContextDefaults fills these correctly when called.
	ctx.ApplyContextDefaults()
	if ctx.Strategy != "smart" {
		t.Errorf("After ApplyContextDefaults: Strategy = %q, want 'smart'", ctx.Strategy)
	}
	if ctx.CompactThreshold != 0.8 {
		t.Errorf("After ApplyContextDefaults: CompactThreshold = %v, want 0.8", ctx.CompactThreshold)
	}
	if ctx.FallbackCtxSize != 128000 {
		t.Errorf("After ApplyContextDefaults: FallbackCtxSize = %d, want 128000", ctx.FallbackCtxSize)
	}
}

func TestContextConfig_YAMLRoundTrip_MaxTokensAutoString(t *testing.T) {
	// max_tokens: "auto" → ResolveMaxTokens() returns 0 (auto-detect signal)
	yamlData := `
provider:
  api_key: "test-key"
agent:
  max_iterations: 5
  context:
    max_tokens: "auto"
    strategy: "smart"
`
	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	if v := cfg.Agent.Context.ResolveMaxTokens(); v != 0 {
		t.Errorf("max_tokens 'auto': ResolveMaxTokens() = %d, want 0", v)
	}
}

func TestContextConfig_YAMLRoundTrip_NoneStrategy(t *testing.T) {
	yamlData := `
provider:
  api_key: "test-key"
agent:
  max_iterations: 5
  context:
    strategy: "none"
`
	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load expected no error, got: %v", err)
	}

	if cfg.Agent.Context.Strategy != "none" {
		t.Errorf("strategy 'none': got %q", cfg.Agent.Context.Strategy)
	}
}

func TestContextConfig_ApplyDefaults_CompactThresholdZero_GetsDefault(t *testing.T) {
	// CompactThreshold == 0.0 → ApplyContextDefaults fills 0.8
	c := ContextConfig{CompactThreshold: 0}
	c.ApplyContextDefaults()
	if c.CompactThreshold != 0.8 {
		t.Errorf("CompactThreshold 0 → default: got %v, want 0.8", c.CompactThreshold)
	}
}

func TestContextConfig_ApplyDefaults_NegativeProtectedTurns_NotChanged(t *testing.T) {
	// ApplyContextDefaults only fills zero-value fields.
	// Negative ProtectedTurns is non-zero, so it is NOT overwritten by ApplyContextDefaults.
	// The compaction logic itself handles negative values gracefully (treated as 0).
	c := ContextConfig{ProtectedTurns: -3}
	c.ApplyContextDefaults()
	// ProtectedTurns is -3 (non-zero) so it is preserved (no default applied).
	if c.ProtectedTurns != -3 {
		t.Errorf("negative ProtectedTurns: expected -3 preserved, got %d", c.ProtectedTurns)
	}
}

func TestContextConfig_ApplyDefaults_ToolResultMaxCharsSmall_NotChanged(t *testing.T) {
	// ToolResultMaxChars < 100 but non-zero: ApplyContextDefaults leaves it unchanged.
	c := ContextConfig{ToolResultMaxChars: 50}
	c.ApplyContextDefaults()
	if c.ToolResultMaxChars != 50 {
		t.Errorf("ToolResultMaxChars 50: expected 50 preserved, got %d", c.ToolResultMaxChars)
	}
}

func TestContextConfig_ApplyDefaults_UnknownStrategy_Preserved(t *testing.T) {
	// An unknown non-empty strategy is preserved (no validation at default-apply level).
	c := ContextConfig{Strategy: "turbo"}
	c.ApplyContextDefaults()
	if c.Strategy != "turbo" {
		t.Errorf("unknown strategy 'turbo': expected preserved, got %q", c.Strategy)
	}
}

// marshalConfig marshals a Config to YAML bytes (test helper for round-trip tests).
func marshalConfig(cfg *Config) ([]byte, error) {
	return yaml.Marshal(cfg)
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "daimon-config-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	if _, err := f.Write([]byte(strings.TrimSpace(content))); err != nil {
		t.Fatalf("failed to write to temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

// TestIsProviderConfigured_TruthTable — covers AS-5 through AS-10 (v2 shape).
func TestIsProviderConfigured_TruthTable(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantOK      bool
		wantMissing []string // substrings that must appear in at least one missing field
	}{
		{
			// AS-5: providers nil, models zeroed → false
			name:        "AS-5 providers nil, models zero",
			cfg:         Config{},
			wantOK:      false,
			wantMissing: []string{"models.default.provider"},
		},
		{
			// AS-6: model missing → false
			name: "AS-6 provider set, model empty",
			cfg: Config{
				Providers: map[string]ProviderCredentials{"openrouter": {APIKey: "sk-or-abc"}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "openrouter", Model: ""}},
			},
			wantOK:      false,
			wantMissing: []string{"models.default.model"},
		},
		{
			// AS-7: api_key empty, non-ollama → false
			name: "AS-7 empty api_key non-ollama",
			cfg: Config{
				Providers: map[string]ProviderCredentials{"openrouter": {APIKey: ""}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"}},
			},
			wantOK:      false,
			wantMissing: []string{"providers.openrouter.api_key"},
		},
		{
			// AS-8: fully configured → true
			name: "AS-8 fully configured openrouter",
			cfg: Config{
				Providers: map[string]ProviderCredentials{"openrouter": {APIKey: "sk-or-abc"}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"}},
			},
			wantOK:      true,
			wantMissing: nil,
		},
		{
			// AS-9: active provider not in Providers map → false
			name: "AS-9 active provider absent from map",
			cfg: Config{
				Providers: map[string]ProviderCredentials{"openrouter": {APIKey: "sk-or-abc"}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-opus-4-6"}},
			},
			wantOK:      false,
			wantMissing: []string{"providers.anthropic.api_key"},
		},
		{
			// AS-10: ollama exemption — empty api_key is OK
			name: "AS-10 ollama exemption",
			cfg: Config{
				Providers: map[string]ProviderCredentials{"ollama": {APIKey: ""}},
				Models:    ModelsConfig{Default: ModelRef{Provider: "ollama", Model: "llama3"}},
			},
			wantOK:      true,
			wantMissing: nil,
		},
		{
			// Multiple missing: provider + model both empty
			name:        "provider and model both empty",
			cfg:         Config{Providers: map[string]ProviderCredentials{}},
			wantOK:      false,
			wantMissing: []string{"models.default.provider", "models.default.model"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, missing := IsProviderConfigured(tc.cfg)

			if ok != tc.wantOK {
				t.Errorf("IsProviderConfigured() ok = %v, want %v (missing: %v)", ok, tc.wantOK, missing)
			}

			if tc.wantMissing == nil {
				if len(missing) != 0 {
					t.Errorf("expected no missing fields, got %v", missing)
				}
				return
			}

			// Verify each expected substring appears in at least one missing field.
			for _, want := range tc.wantMissing {
				found := false
				for _, m := range missing {
					if strings.Contains(m, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected missing field containing %q, got %v", want, missing)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T-12 — ApplyDefaults and validate updates (v2 shape)
// ---------------------------------------------------------------------------

// TestApplyDefaults_V2Shape — ApplyDefaults should NOT modify the credentials map.
// Timeout/MaxRetries/Stream defaults are applied by ResolveActiveProvider, not stored.
func TestApplyDefaults_V2Shape(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-test"},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-opus-4-6"}},
	}
	cfg.ApplyDefaults()

	// Credentials map must remain unchanged (no timeout/retries injected).
	if cfg.Providers["anthropic"].APIKey != "sk-ant-test" {
		t.Errorf("Providers[anthropic].APIKey changed by ApplyDefaults")
	}

	// ResolveActiveProvider must return defaults.
	resolved := ResolveActiveProvider(*cfg)
	if resolved.Timeout != 60*time.Second {
		t.Errorf("resolved.Timeout = %v, want 60s", resolved.Timeout)
	}
	if resolved.MaxRetries != 3 {
		t.Errorf("resolved.MaxRetries = %d, want 3", resolved.MaxRetries)
	}
	if resolved.Stream == nil || !*resolved.Stream {
		t.Errorf("resolved.Stream = %v, want pointer-to-true", resolved.Stream)
	}
}

// TestValidate_SkipsAPIKeyCheckWhenNoActiveProvider — OQ-3: empty Models.Default.Provider
// skips api_key check entirely (setup-only mode).
func TestValidate_SkipsAPIKeyCheckWhenNoActiveProvider(t *testing.T) {
	cfg := &Config{
		// No Providers, no Models.Default.Provider — setup-only state.
	}
	cfg.ApplyDefaults()

	// validate() must not error on missing api_key.
	if err := cfg.validate(); err != nil {
		t.Errorf("expected no error in setup-only mode (no active provider), got: %v", err)
	}
}

// TestValidate_V2HappyPath — full v2 config passes validation.
func TestValidate_V2HappyPath(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderCredentials{
			"openrouter": {APIKey: "sk-or-abc"},
			"anthropic":  {APIKey: "sk-ant-xyz"},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"}},
	}
	cfg.ApplyDefaults()
	if err := cfg.validate(); err != nil {
		t.Errorf("expected no validation error for valid v2 config, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// T-14 — Round-trip write test (AS-4)
// ---------------------------------------------------------------------------

// TestLoadWriteRoundTrip_V1InV2Out — Load v1 YAML, marshal output must be v2.
func TestLoadWriteRoundTrip_V1InV2Out(t *testing.T) {
	v1YAML := `
provider:
  type: openrouter
  model: anthropic/claude-haiku-4.5
  api_key: sk-or-abc
agent:
  max_iterations: 5
`
	tmpFile := createTempFile(t, v1YAML)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out, err := marshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	outStr := string(out)

	if !strings.Contains(outStr, "providers:") {
		t.Errorf("marshaled YAML does not contain 'providers:'\n%s", outStr)
	}
	if !strings.Contains(outStr, "models:") {
		t.Errorf("marshaled YAML does not contain 'models:'\n%s", outStr)
	}
	// The top-level "provider:" key must NOT appear (pointer+omitempty ensures this).
	lines := strings.Split(outStr, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "provider:") {
			t.Errorf("marshaled YAML still contains top-level 'provider:' key:\n%s", outStr)
			break
		}
	}
}

// TestAtomicWriteConfig_BackupOnFirstV2Save covers T-69/T-70 backup semantics.
//
// Case A: v1 file on disk → atomicWriteConfig with v2 config → .v1.bak created
//         with original v1 content, path now contains v2 content.
// Case B: v2 file already on disk → atomicWriteConfig again → NO new .v1.bak
//         (backup is once-per-file-upgrade, not per-save).
// Case C: file does not exist (fresh install) → atomicWriteConfig → NO .v1.bak.
func TestAtomicWriteConfig_BackupOnFirstV2Save(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	bakPath := cfgPath + ".v1.bak"

	v1YAML := []byte(`provider:
  type: openrouter
  model: anthropic/claude-haiku-4.5
  api_key: sk-or-v1key
agent:
  name: TestAgent
`)

	v2Cfg := &Config{
		Providers: map[string]ProviderCredentials{
			"openrouter": {APIKey: "sk-or-v2key"},
		},
		Models: ModelsConfig{
			Default: ModelRef{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"},
		},
	}

	// ── Case A: v1 on disk ──────────────────────────────────────────────────
	if err := os.WriteFile(cfgPath, v1YAML, 0o600); err != nil {
		t.Fatalf("write v1 file: %v", err)
	}

	if err := AtomicWriteConfig(cfgPath, v2Cfg); err != nil {
		t.Fatalf("AtomicWriteConfig (case A): %v", err)
	}

	// Backup must exist and contain original v1 content.
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("case A: .v1.bak not created: %v", err)
	}
	if string(bakData) != string(v1YAML) {
		t.Errorf("case A: .v1.bak content mismatch\ngot:  %q\nwant: %q", string(bakData), string(v1YAML))
	}

	// Main file must now contain v2 content (has "providers:" key).
	written, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("case A: read written config: %v", err)
	}
	if !strings.Contains(string(written), "providers:") {
		t.Errorf("case A: written config missing 'providers:' key:\n%s", string(written))
	}

	// ── Case B: v2 already on disk (no re-backup) ───────────────────────────
	// Remove the bak to check it isn't re-created.
	if err := os.Remove(bakPath); err != nil {
		t.Fatalf("remove .v1.bak for case B: %v", err)
	}

	if err := AtomicWriteConfig(cfgPath, v2Cfg); err != nil {
		t.Fatalf("AtomicWriteConfig (case B): %v", err)
	}

	if _, err := os.Stat(bakPath); err == nil {
		t.Error("case B: .v1.bak was re-created; backup should only happen on first v2 save")
	}

	// ── Case C: fresh install (file does not exist) ──────────────────────────
	if err := os.Remove(cfgPath); err != nil {
		t.Fatalf("remove config for case C: %v", err)
	}

	if err := AtomicWriteConfig(cfgPath, v2Cfg); err != nil {
		t.Fatalf("AtomicWriteConfig (case C): %v", err)
	}

	if _, err := os.Stat(bakPath); err == nil {
		t.Error("case C: .v1.bak created for fresh install; should not exist")
	}
}

// TestApplyDefaults_StampsAuthTokenIssuedAtWhenZero verifies FR-55/AS-25:
// when a config is loaded without auth_token_issued_at, ApplyDefaults stamps
// AuthTokenIssuedAt to approximately time.Now() (within 1 second).
func TestApplyDefaults_StampsAuthTokenIssuedAtWhenZero(t *testing.T) {
	before := time.Now()
	var c Config
	// AuthTokenIssuedAt is zero (field absent from YAML — legacy config).
	c.ApplyDefaults()
	after := time.Now()

	if c.Web.AuthTokenIssuedAt.IsZero() {
		t.Fatal("ApplyDefaults: AuthTokenIssuedAt must not be zero for a legacy config")
	}
	if c.Web.AuthTokenIssuedAt.Before(before) || c.Web.AuthTokenIssuedAt.After(after) {
		t.Fatalf("ApplyDefaults: AuthTokenIssuedAt %v not in [%v, %v]",
			c.Web.AuthTokenIssuedAt, before, after)
	}
}

// TestApplyDefaults_PreservesExistingAuthTokenIssuedAt verifies FR-54/NFR-4:
// when auth_token_issued_at is already set, ApplyDefaults must not overwrite it.
func TestApplyDefaults_PreservesExistingAuthTokenIssuedAt(t *testing.T) {
	existing := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var c Config
	c.Web.AuthTokenIssuedAt = existing
	c.ApplyDefaults()

	if !c.Web.AuthTokenIssuedAt.Equal(existing) {
		t.Fatalf("ApplyDefaults: AuthTokenIssuedAt mutated from %v to %v",
			existing, c.Web.AuthTokenIssuedAt)
	}
}

// ---------------------------------------------------------------------------
// Phase 2 rename — daimon paths, env vars, agent name
// ---------------------------------------------------------------------------

func TestApplyDefaults_StorePathIsDaimon(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Store.Path != "~/.daimon/data" {
		t.Errorf("Store.Path = %q, want %q", cfg.Store.Path, "~/.daimon/data")
	}
}

func TestApplyDefaults_AuditPathIsDaimon(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Audit.Path != "~/.daimon/audit" {
		t.Errorf("Audit.Path = %q, want %q", cfg.Audit.Path, "~/.daimon/audit")
	}
}

func TestApplyDefaults_SkillsDirIsDaimon(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.SkillsDir != "~/.daimon/skills" {
		t.Errorf("SkillsDir = %q, want %q", cfg.SkillsDir, "~/.daimon/skills")
	}
}

func TestApplyDefaults_AgentNameIsDaimon(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Agent.Name != "Daimon" {
		t.Errorf("Agent.Name = %q, want %q", cfg.Agent.Name, "Daimon")
	}
}

func TestApplyDefaults_JinaEnvVarIsDaimon(t *testing.T) {
	t.Setenv("DAIMON_JINA_API_KEY", "test-jina-key")
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Tools.WebFetch.JinaAPIKey != "test-jina-key" {
		t.Errorf("JinaAPIKey = %q, want %q from DAIMON_JINA_API_KEY", cfg.Tools.WebFetch.JinaAPIKey, "test-jina-key")
	}
}

func TestApplyDefaults_WebTokenEnvVarIsDaimon(t *testing.T) {
	t.Setenv("DAIMON_WEB_TOKEN", "test-web-token")
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Web.AuthToken != "test-web-token" {
		t.Errorf("Web.AuthToken = %q, want %q from DAIMON_WEB_TOKEN", cfg.Web.AuthToken, "test-web-token")
	}
}

func TestFindConfigPath_LooksInDaimonDir(t *testing.T) {
	tmpDir := t.TempDir()
	daimonDir := filepath.Join(tmpDir, ".daimon")
	if err := os.MkdirAll(daimonDir, 0o755); err != nil {
		t.Fatalf("mkdir .daimon: %v", err)
	}
	cfgPath := filepath.Join(daimonDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("agent:\n  max_iterations: 5\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Override home dir via env — FindConfigPath uses os.UserHomeDir.
	t.Setenv("HOME", tmpDir)
	found, err := FindConfigPath("")
	if err != nil {
		t.Fatalf("FindConfigPath: %v", err)
	}
	if found != cfgPath {
		t.Errorf("FindConfigPath() = %q, want %q", found, cfgPath)
	}
}

// --------------------------------------------------------------------------
// Phase 1.3 — Anthropic thinking config keys
// --------------------------------------------------------------------------

func TestProviderCredentials_AnthropicThinkingKeys(t *testing.T) {
	tests := []struct {
		name               string
		yaml               string
		wantEffort         string
		wantBudgetTokens   *int
		wantErr            bool
	}{
		{
			name: "thinking_effort and thinking_budget_tokens parse correctly",
			yaml: `
providers:
  anthropic:
    api_key: "sk-ant-test"
    thinking_effort: "high"
    thinking_budget_tokens: 15000
models:
  default:
    provider: anthropic
    model: claude-opus-4-7
`,
			wantEffort:       "high",
			wantBudgetTokens: func() *int { v := 15000; return &v }(),
		},
		{
			name: "zero-value struct has no thinking keys set",
			yaml: `
providers:
  anthropic:
    api_key: "sk-ant-test"
models:
  default:
    provider: anthropic
    model: claude-opus-4-6
`,
			wantEffort:       "",
			wantBudgetTokens: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile := createTempFile(t, tt.yaml)
			defer os.Remove(tmpFile)

			cfg, err := Load(tmpFile)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}

			creds := cfg.Providers["anthropic"]
			if creds.ThinkingEffort != tt.wantEffort {
				t.Errorf("ThinkingEffort = %q, want %q", creds.ThinkingEffort, tt.wantEffort)
			}
			if tt.wantBudgetTokens == nil {
				if creds.ThinkingBudgetTokens != nil {
					t.Errorf("ThinkingBudgetTokens = %v, want nil", creds.ThinkingBudgetTokens)
				}
			} else {
				if creds.ThinkingBudgetTokens == nil {
					t.Fatalf("ThinkingBudgetTokens is nil, want %d", *tt.wantBudgetTokens)
				}
				if *creds.ThinkingBudgetTokens != *tt.wantBudgetTokens {
					t.Errorf("ThinkingBudgetTokens = %d, want %d", *creds.ThinkingBudgetTokens, *tt.wantBudgetTokens)
				}
			}
		})
	}
}

// T12 (canonical config): ApplyDefaults fills HyDE non-bool fields when zero.
func TestApplyDefaults_RAGHyDEDefaults(t *testing.T) {
	c := &Config{}
	c.ApplyDefaults()

	if c.RAG.Hyde.HypothesisTimeout != 10*time.Second {
		t.Errorf("Hyde.HypothesisTimeout: want 10s, got %v", c.RAG.Hyde.HypothesisTimeout)
	}
	if c.RAG.Hyde.QueryWeight != 0.3 {
		t.Errorf("Hyde.QueryWeight: want 0.3, got %v", c.RAG.Hyde.QueryWeight)
	}
	if c.RAG.Hyde.MaxCandidates != 20 {
		t.Errorf("Hyde.MaxCandidates: want 20, got %d", c.RAG.Hyde.MaxCandidates)
	}
}

// T13 (canonical config): ApplyDefaults does NOT enable HyDE.
func TestApplyDefaults_RAGHyDE_DisabledByDefault(t *testing.T) {
	c := &Config{}
	c.ApplyDefaults()

	if c.RAG.Hyde.Enabled {
		t.Error("RAG.Hyde.Enabled must be false by default (opt-in)")
	}
}
