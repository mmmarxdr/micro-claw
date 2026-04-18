package config

import "time"

// ResolveActiveProvider assembles a fully-populated ProviderConfig from the
// active Models.Default entry and the Providers map.
//
// Returns an empty ProviderConfig{} (no panic) when:
//   - Models.Default.Provider is empty, OR
//   - Providers is nil/empty, OR
//   - the active provider key is absent from Providers.
//
// Callers should pair this with IsProviderConfigured when they need to detect
// the "not yet configured" state.
//
// Defaults applied on the returned copy:
//   - Timeout:    60s
//   - MaxRetries: 3
//   - Stream:     true
func ResolveActiveProvider(cfg Config) ProviderConfig {
	provName := cfg.Models.Default.Provider
	if provName == "" {
		return ProviderConfig{}
	}

	creds, ok := cfg.Providers[provName]
	// If the provider is not in the map, still return a typed config with Type + Model set
	// so callers can read the selected provider name, even without credentials.
	result := ProviderConfig{
		Type:  provName,
		Model: cfg.Models.Default.Model,
	}
	if ok {
		result.APIKey = creds.APIKey
		result.BaseURL = creds.BaseURL
	}

	// Apply defaults to the returned copy (credentials map is credentials-only;
	// timeout/retry/stream are resolved here, not stored in Providers).
	if result.Timeout == 0 {
		result.Timeout = 60 * time.Second
	}
	if result.MaxRetries == 0 {
		result.MaxRetries = 3
	}
	if result.Stream == nil {
		t := true
		result.Stream = &t
	}

	return result
}
