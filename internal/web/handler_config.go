package web

import (
	"net/http"

	"microagent/internal/config"
)

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := *s.deps.Config // shallow copy

	// Mask all provider api_keys in the v2 Providers map.
	if cfg.Providers != nil {
		masked := make(map[string]config.ProviderCredentials, len(cfg.Providers))
		for name, creds := range cfg.Providers {
			creds.APIKey = config.MaskSecret(creds.APIKey)
			masked[name] = creds
		}
		cfg.Providers = masked
	}

	// Also mask the legacy Provider pointer api_key if present (defensive).
	if cfg.Provider != nil {
		p := *cfg.Provider
		p.APIKey = config.MaskSecret(p.APIKey)
		cfg.Provider = &p
	}

	// Mask channel tokens and web auth token.
	cfg.Channel.Token = config.MaskSecret(cfg.Channel.Token)
	cfg.Channel.AccessToken = config.MaskSecret(cfg.Channel.AccessToken)
	cfg.Channel.VerifyToken = config.MaskSecret(cfg.Channel.VerifyToken)
	cfg.Web.AuthToken = config.MaskSecret(cfg.Web.AuthToken)

	// Mask fallback api_key (now on Config directly, per OQ-4).
	if cfg.Fallback != nil {
		fb := *cfg.Fallback
		fb.APIKey = config.MaskSecret(fb.APIKey)
		cfg.Fallback = &fb
	}

	writeJSON(w, http.StatusOK, cfg)
}
