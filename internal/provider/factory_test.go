package provider_test

import (
	"testing"

	"daimon/internal/config"
	"daimon/internal/provider"
)

func TestNewFromConfig_KnownTypes(t *testing.T) {
	tests := []struct {
		name        string
		provType    string
		needsAPIKey bool
	}{
		{"anthropic", "anthropic", true},
		{"openai", "openai", true},
		{"gemini", "gemini", true},
		{"openrouter", "openrouter", true},
		{"ollama", "ollama", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.ProviderConfig{
				Type:  tc.provType,
				Model: "test-model",
			}
			if tc.needsAPIKey {
				cfg.APIKey = "test-key"
			} else {
				cfg.BaseURL = "http://localhost:11434"
			}

			p, err := provider.NewFromConfig(cfg)
			if err != nil {
				t.Fatalf("NewFromConfig(%q): unexpected error: %v", tc.provType, err)
			}
			if p == nil {
				t.Fatalf("NewFromConfig(%q): returned nil provider", tc.provType)
			}
		})
	}
}

func TestNewFromConfig_UnknownType(t *testing.T) {
	cfg := config.ProviderConfig{
		Type:   "nonexistent-provider",
		Model:  "some-model",
		APIKey: "some-key",
	}
	p, err := provider.NewFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider type, got nil")
	}
	if p != nil {
		t.Fatal("expected nil provider for unknown type")
	}
}

func TestNewFromConfig_EmptyTypeIsError(t *testing.T) {
	cfg := config.ProviderConfig{
		Model:  "some-model",
		APIKey: "some-key",
	}
	_, err := provider.NewFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for empty provider type, got nil")
	}
}
