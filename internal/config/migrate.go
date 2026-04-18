package config

import "log"

// MigrateLegacyProviderPublic is the exported wrapper for migrateLegacyProvider.
// Use this when you have already unmarshaled a Config and need to apply migration
// outside of the Load function (e.g., in setup handlers that read and re-marshal).
func MigrateLegacyProviderPublic(cfg *Config) {
	migrateLegacyProvider(cfg)
}

// migrateLegacyProvider reshapes a v1 Config into v2 form in place. Idempotent.
//
// Trigger: cfg.Provider != nil && cfg.Provider.Type != "" &&
//
//	(cfg.Providers == nil || len(cfg.Providers) == 0)
//
// When triggered (pure v1 file):
//  1. Populate cfg.Providers[type] with APIKey + BaseURL from v1 block.
//  2. Set cfg.Models.Default from v1 Type + Model.
//  3. Move cfg.Provider.Fallback → cfg.Fallback if applicable (OQ-4).
//  4. Set cfg.Provider = nil so yaml.Marshal omits the legacy key via omitempty.
//
// Mixed v1+v2 (both provider: and providers: present):
// v2 wins — trigger is false because Providers is already non-empty.
// The legacy pointer is still nilled to prevent re-emission on the next save.
func migrateLegacyProvider(cfg *Config) {
	if cfg.Provider == nil {
		return
	}

	// Mixed case: v2 already populated. Nil the legacy pointer and exit.
	if len(cfg.Providers) > 0 {
		cfg.Provider = nil
		return
	}

	// Pure v1 case: trigger.
	if cfg.Provider.Type == "" {
		// Pointer is set but Type is empty — nothing to migrate.
		cfg.Provider = nil
		return
	}

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderCredentials)
	}
	cfg.Providers[cfg.Provider.Type] = ProviderCredentials{
		APIKey:  cfg.Provider.APIKey,
		BaseURL: cfg.Provider.BaseURL,
	}
	cfg.Models.Default = ModelRef{
		Provider: cfg.Provider.Type,
		Model:    cfg.Provider.Model,
	}

	// OQ-4: migrate Fallback from Provider to Config top-level.
	if cfg.Provider.Fallback != nil && cfg.Fallback == nil {
		cfg.Fallback = cfg.Provider.Fallback
	}

	log.Printf("config: migrated legacy provider block into providers/models (v1→v2); file will be rewritten on next save")
	cfg.Provider = nil
}
