package config

import (
	"testing"
	"time"
)

func TestResolveActiveProvider_Happy(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-abc", BaseURL: ""},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-opus-4-6"}},
	}

	got := ResolveActiveProvider(cfg)

	if got.Type != "anthropic" {
		t.Errorf("Type = %q, want anthropic", got.Type)
	}
	if got.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want claude-opus-4-6", got.Model)
	}
	if got.APIKey != "sk-ant-abc" {
		t.Errorf("APIKey = %q, want sk-ant-abc", got.APIKey)
	}
}

func TestResolveActiveProvider_EmptyProvider(t *testing.T) {
	cfg := Config{}

	got := ResolveActiveProvider(cfg)

	// Must return zero-value, no panic.
	if got.Type != "" {
		t.Errorf("Type = %q, want empty", got.Type)
	}
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", got.APIKey)
	}
}

func TestResolveActiveProvider_MissingEntry(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderCredentials{
			"openrouter": {APIKey: "sk-or-abc"},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "anthropic", Model: "claude-opus-4-6"}},
	}

	got := ResolveActiveProvider(cfg)

	// Provider not in map → zero-value ProviderConfig (FR-12).
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty (missing provider entry)", got.APIKey)
	}
	// Type should still be populated from Models.Default.Provider.
	if got.Type != "anthropic" {
		t.Errorf("Type = %q, want anthropic", got.Type)
	}
}

func TestResolveActiveProvider_DefaultsApplied(t *testing.T) {
	cfg := Config{
		Providers: map[string]ProviderCredentials{
			"gemini": {APIKey: "AIzaXXX"},
		},
		Models: ModelsConfig{Default: ModelRef{Provider: "gemini", Model: "gemini-2.5-pro"}},
	}

	got := ResolveActiveProvider(cfg)

	if got.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v, want 60s", got.Timeout)
	}
	if got.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", got.MaxRetries)
	}
	if got.Stream == nil || !*got.Stream {
		t.Errorf("Stream = %v, want pointer-to-true", got.Stream)
	}
}
