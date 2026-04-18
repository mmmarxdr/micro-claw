package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"microagent/internal/config"
)

func TestWriteConfig_CreatesFileWithCorrectPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func TestWriteConfig_CreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "config.yaml")

	cfg := minimalConfig()
	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist at %s: %v", path, err)
	}
}

func TestWriteConfig_AtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg1 := minimalConfig()
	cfg1.Agent.Name = "first"
	if err := WriteConfig(path, cfg1); err != nil {
		t.Fatalf("WriteConfig (first): %v", err)
	}

	cfg2 := minimalConfig()
	cfg2.Agent.Name = "second"
	if err := WriteConfig(path, cfg2); err != nil {
		t.Fatalf("WriteConfig (second): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if content == "" {
		t.Error("expected non-empty content after overwrite")
	}
}

func TestWriteConfig_WrittenYAMLIsLoadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{
		"anthropic": {APIKey: "test-key-123"},
	}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}}
	cfg.Channel.Type = "cli"
	cfg.Store.Type = "sqlite"
	cfg.Store.Path = dir

	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	wantAPIKey := cfg.Providers["anthropic"].APIKey
	gotAPIKey := loaded.Providers["anthropic"].APIKey
	if gotAPIKey != wantAPIKey {
		t.Errorf("Providers[anthropic].APIKey = %q, want %q", gotAPIKey, wantAPIKey)
	}
	wantModel := cfg.Models.Default.Model
	gotModel := loaded.Models.Default.Model
	if gotModel != wantModel {
		t.Errorf("Models.Default.Model = %q, want %q", gotModel, wantModel)
	}
	if loaded.Channel.Type != cfg.Channel.Type {
		t.Errorf("Channel.Type = %q, want %q", loaded.Channel.Type, cfg.Channel.Type)
	}
}

func TestDefaultConfigPath_ReturnsHomeBased(t *testing.T) {
	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if path == "" {
		t.Error("expected non-empty path")
	}
	// Should end with .microagent/config.yaml
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("expected path to end with config.yaml, got %q", path)
	}
}

func TestDetectConfigPath_DetectsLocalConfig(t *testing.T) {
	// Create a temporary directory and simulate being in it
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Change to temp directory
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}

	// Create a local config.yaml
	localConfigPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(localConfigPath, []byte("# test config"), 0644); err != nil {
		t.Fatalf("write local config.yaml: %v", err)
	}

	path, err := DetectConfigPath()
	if err != nil {
		t.Fatalf("DetectConfigPath: %v", err)
	}

	// Should detect local config.yaml and return it
	expected := localConfigPath
	if path != expected {
		t.Errorf("DetectConfigPath() = %q, want %q (local config.yaml)", path, expected)
	}
}

func TestDetectConfigPath_FallsBackToDefaultWhenNoLocalConfig(t *testing.T) {
	// Create a temporary directory without config.yaml
	tmpDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	// Clear XDG_CONFIG_HOME so the function falls through to DefaultConfigPath
	t.Setenv("XDG_CONFIG_HOME", "")

	// Change to temp directory
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}

	path, err := DetectConfigPath()
	if err != nil {
		t.Fatalf("DetectConfigPath: %v", err)
	}

	// Should return default path
	defaultPath, _ := DefaultConfigPath()
	if path != defaultPath {
		t.Errorf("DetectConfigPath() = %q, want default %q", path, defaultPath)
	}
}

func TestDetectConfigPath_UsesXDGWhenSet(t *testing.T) {
	// Save original env var
	originalXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

	// Create temp dir for XDG_CONFIG_HOME
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	path, err := DetectConfigPath()
	if err != nil {
		t.Fatalf("DetectConfigPath: %v", err)
	}

	// Should use XDG path
	expected := filepath.Join(tmpDir, "microagent", "config.yaml")
	if path != expected {
		t.Errorf("DetectConfigPath() = %q, want XDG path %q", path, expected)
	}
}

func TestDetectConfigPath_PrefersLocalOverXDG(t *testing.T) {
	// Save original env var
	originalXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

	// Create temp dir for XDG_CONFIG_HOME
	xdgDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", xdgDir)

	// Create another temp dir with local config.yaml
	localDir := t.TempDir()
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)

	if err := os.Chdir(localDir); err != nil {
		t.Fatalf("chdir to local dir: %v", err)
	}

	// Create a local config.yaml
	localConfigPath := filepath.Join(localDir, "config.yaml")
	if err := os.WriteFile(localConfigPath, []byte("# local config"), 0644); err != nil {
		t.Fatalf("write local config.yaml: %v", err)
	}

	path, err := DetectConfigPath()
	if err != nil {
		t.Fatalf("DetectConfigPath: %v", err)
	}

	// Should prefer local config.yaml over XDG
	if path != localConfigPath {
		t.Errorf("DetectConfigPath() = %q, want local config %q", path, localConfigPath)
	}
}

func TestWriteConfig_AllowedUsersInYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Channel.Type = "telegram"
	cfg.Channel.Token = "bot-token-xyz"
	cfg.Channel.AllowedUsers = []int64{111, 222}

	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	if !contains(content, "allowed_users") {
		t.Error("expected 'allowed_users' key in YAML output")
	}
	if !contains(content, "111") {
		t.Error("expected user ID 111 in YAML output")
	}
	if !contains(content, "222") {
		t.Error("expected user ID 222 in YAML output")
	}
}

func TestWriteConfig_AllowedUsersEmptyList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Channel.Type = "telegram"
	cfg.Channel.Token = "bot-token-xyz"
	// AllowedUsers deliberately left nil/empty

	if err := WriteConfig(path, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	// The field should still appear as an empty sequence
	if !contains(content, "allowed_users") {
		t.Error("expected 'allowed_users' key even when list is empty")
	}
}

// contains is a simple substring check helper.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// minimalConfig returns a minimal config suitable for WriteConfig tests (v2 shape).
func minimalConfig() *config.Config {
	return &config.Config{
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-test"},
		},
		Models: config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}},
		Channel: config.ChannelConfig{
			Type: "cli",
		},
		Store: config.StoreConfig{
			Type: "sqlite",
			Path: "~/.microagent/data",
		},
		Audit: config.AuditConfig{
			Enabled: true,
			Type:    "sqlite",
			Path:    "~/.microagent/audit",
		},
	}
}
