package provider

import (
	"log/slog"
	"sync"
	"time"

	"daimon/internal/config"
)

// Registry maps provider names to their ModelLister implementations.
// It is the single point of resolution for model listing at runtime.
//
// Two populations:
//  1. Static — built once at startup from cfg.Providers (NewStaticRegistry).
//  2. Transient — registered after startup, e.g. after setup-wizard validate-key.
//     Transient registrations overwrite static ones for the same name.
type Registry struct {
	mu      sync.RWMutex
	listers map[string]ModelLister
}

// NewStaticRegistry constructs a Registry from the full config.
// Only providers that have API keys configured are registered (ollama is exempt).
// Providers that implement both Provider and ModelLister are admitted;
// providers that don't support ListModels are silently skipped.
func NewStaticRegistry(cfg config.Config) *Registry {
	r := &Registry{
		listers: make(map[string]ModelLister),
	}

	for name, creds := range cfg.Providers {
		// Skip providers without API keys (except ollama).
		if name != "ollama" && creds.APIKey == "" {
			continue
		}

		provCfg := config.ProviderConfig{
			Type:    name,
			APIKey:  creds.APIKey,
			BaseURL: creds.BaseURL,
			Timeout: 60 * time.Second,
		}

		p, err := NewFromConfig(provCfg)
		if err != nil {
			slog.Warn("registry: failed to construct provider, skipping",
				"provider", name, "error", err)
			continue
		}

		// Wire Anthropic thinking config if configured.
		if ap, ok := p.(*AnthropicProvider); ok {
			ap.SetThinkingConfig(creds)
		}

		// Wire OpenRouter ModelInfoStore if available (cache is wired later via SetModelInfoStore).
		// The caller can update this after construction with RegisterTransient.

		ml, ok := p.(ModelLister)
		if !ok {
			// Provider doesn't support model listing — skip.
			continue
		}

		r.listers[name] = ml
	}

	return r
}

// Lister returns the ModelLister for the given provider name.
// Returns (nil, false) if the provider is not registered.
func (r *Registry) Lister(name string) (ModelLister, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ml, ok := r.listers[name]
	return ml, ok
}

// RegisterTransient adds or replaces a ModelLister for name.
// Used by the setup wizard after a successful validate-key to make the provider
// available without restarting the server.
// If p does not implement ModelLister, it is silently ignored.
func (r *Registry) RegisterTransient(name string, p Provider) {
	ml, ok := p.(ModelLister)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listers[name] = ml
}
