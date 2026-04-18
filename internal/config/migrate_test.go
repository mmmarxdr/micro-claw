package config

import (
	"bytes"
	"log"
	"testing"
)

// TestMigrateLegacyProvider_V1 — AS-1: v1 config migrates correctly.
// After migration: Provider pointer is nil, Providers map and Models.Default populated,
// and Config.Fallback populated if Provider.Fallback was set.
func TestMigrateLegacyProvider_V1(t *testing.T) {
	cfg := &Config{
		Provider: &ProviderConfig{
			Type:    "openrouter",
			Model:   "anthropic/claude-haiku-4.5",
			APIKey:  "sk-or-abc",
			BaseURL: "",
		},
	}

	migrateLegacyProvider(cfg)

	if cfg.Provider != nil {
		t.Error("expected Provider pointer to be nil after migration")
	}
	if cfg.Models.Default.Provider != "openrouter" {
		t.Errorf("Models.Default.Provider = %q, want %q", cfg.Models.Default.Provider, "openrouter")
	}
	if cfg.Models.Default.Model != "anthropic/claude-haiku-4.5" {
		t.Errorf("Models.Default.Model = %q, want %q", cfg.Models.Default.Model, "anthropic/claude-haiku-4.5")
	}
	creds, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatal("Providers[openrouter] not found after migration")
	}
	if creds.APIKey != "sk-or-abc" {
		t.Errorf("Providers[openrouter].APIKey = %q, want %q", creds.APIKey, "sk-or-abc")
	}
}

// TestMigrateLegacyProvider_V2_NoOp — AS-2: v2 config with Providers already set is a no-op.
func TestMigrateLegacyProvider_V2_NoOp(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-existing"},
		},
		Models: ModelsConfig{
			Default: ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"},
		},
	}

	migrateLegacyProvider(cfg)

	// Should remain unchanged.
	if len(cfg.Providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(cfg.Providers))
	}
	if cfg.Providers["anthropic"].APIKey != "sk-ant-existing" {
		t.Errorf("Providers[anthropic].APIKey changed unexpectedly")
	}
	if cfg.Models.Default.Provider != "anthropic" {
		t.Errorf("Models.Default.Provider changed unexpectedly")
	}
}

// TestMigrateLegacyProvider_Mixed_V2Wins — AS-3: both provider+providers present → v2 wins.
func TestMigrateLegacyProvider_Mixed_V2Wins(t *testing.T) {
	cfg := &Config{
		Provider: &ProviderConfig{
			Type:   "openrouter",
			Model:  "some-model",
			APIKey: "sk-or-legacy",
		},
		Providers: map[string]ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-v2"},
		},
		Models: ModelsConfig{
			Default: ModelRef{Provider: "anthropic", Model: "claude-opus-4-6"},
		},
	}

	migrateLegacyProvider(cfg)

	// v2 wins: Provider pointer nilled, Providers and Models unchanged.
	if cfg.Provider != nil {
		t.Error("expected Provider pointer to be nil after mixed migration")
	}
	if cfg.Models.Default.Provider != "anthropic" {
		t.Errorf("Models.Default.Provider = %q, want anthropic (v2 wins)", cfg.Models.Default.Provider)
	}
	if cfg.Providers["anthropic"].APIKey != "sk-ant-v2" {
		t.Error("Providers[anthropic].APIKey should be unchanged (v2 wins)")
	}
	// Legacy openrouter key must NOT have been injected.
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Error("Providers[openrouter] should NOT exist — v2 wins and legacy block is discarded")
	}
}

// TestMigrateLegacyProvider_InfoLog — T-71: INFO log is emitted when migration runs.
func TestMigrateLegacyProvider_InfoLog(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	cfg := &Config{
		Provider: &ProviderConfig{
			Type:   "openrouter",
			Model:  "anthropic/claude-haiku-4.5",
			APIKey: "sk-or-test",
		},
	}

	migrateLegacyProvider(cfg)

	if got := buf.String(); got == "" {
		t.Error("expected a log message during v1→v2 migration, got none")
	}
}

// TestMigrateLegacyProvider_FallbackMoved — OQ-4: Provider.Fallback migrated to Config.Fallback.
func TestMigrateLegacyProvider_FallbackMoved(t *testing.T) {
	fb := &FallbackConfig{
		Type:   "anthropic",
		Model:  "claude-haiku-4-6",
		APIKey: "sk-ant-fallback",
	}
	cfg := &Config{
		Provider: &ProviderConfig{
			Type:     "openrouter",
			Model:    "anthropic/claude-haiku-4.5",
			APIKey:   "sk-or-abc",
			Fallback: fb,
		},
	}

	migrateLegacyProvider(cfg)

	if cfg.Fallback == nil {
		t.Fatal("expected Config.Fallback to be populated after migration")
	}
	if cfg.Fallback.APIKey != "sk-ant-fallback" {
		t.Errorf("Config.Fallback.APIKey = %q, want %q", cfg.Fallback.APIKey, "sk-ant-fallback")
	}
	if cfg.Provider != nil {
		t.Error("expected Provider pointer to be nil after migration")
	}
}
