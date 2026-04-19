package provider

import (
	"fmt"

	"daimon/internal/config"
)

// NewFromConfig constructs the appropriate Provider from a ProviderConfig.
// This is the single source of truth for provider instantiation — used by
// both the main agent wiring and the setup wizard's validate-key endpoint.
//
// Returns an error for unknown or empty provider types.
func NewFromConfig(cfg config.ProviderConfig) (Provider, error) {
	switch cfg.Type {
	case "anthropic":
		return NewAnthropicProvider(cfg), nil
	case "gemini":
		return NewGeminiProvider(cfg), nil
	case "openrouter":
		return NewOpenRouterProvider(cfg), nil
	case "openai":
		p, err := NewOpenAIProvider(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize openai provider: %w", err)
		}
		return p, nil
	case "ollama":
		p, err := NewOllamaProvider(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize ollama provider: %w", err)
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", cfg.Type)
	}
}
