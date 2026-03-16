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
	cfg.Provider.Type = "anthropic"
	cfg.Provider.Model = "claude-3-5-sonnet-20241022"
	cfg.Provider.APIKey = "test-key-123"
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

	if loaded.Provider.APIKey != cfg.Provider.APIKey {
		t.Errorf("Provider.APIKey = %q, want %q", loaded.Provider.APIKey, cfg.Provider.APIKey)
	}
	if loaded.Provider.Model != cfg.Provider.Model {
		t.Errorf("Provider.Model = %q, want %q", loaded.Provider.Model, cfg.Provider.Model)
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

// minimalConfig returns a minimal config suitable for WriteConfig tests.
func minimalConfig() *config.Config {
	return &config.Config{
		Provider: config.ProviderConfig{
			Type:   "anthropic",
			Model:  "claude-3-5-sonnet-20241022",
			APIKey: "sk-test",
		},
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
