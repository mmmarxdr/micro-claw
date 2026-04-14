package web

import (
	"net/http"
)

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := *s.deps.Config // shallow copy
	cfg.Provider.APIKey = maskSecret(cfg.Provider.APIKey)
	cfg.Channel.Token = maskSecret(cfg.Channel.Token)
	cfg.Channel.AccessToken = maskSecret(cfg.Channel.AccessToken)
	cfg.Channel.VerifyToken = maskSecret(cfg.Channel.VerifyToken)
	cfg.Web.AuthToken = maskSecret(cfg.Web.AuthToken)
	if cfg.Provider.Fallback != nil {
		fb := *cfg.Provider.Fallback
		fb.APIKey = maskSecret(fb.APIKey)
		cfg.Provider.Fallback = &fb
	}
	writeJSON(w, http.StatusOK, cfg)
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}
