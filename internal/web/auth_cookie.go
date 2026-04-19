package web

import (
	"net/http"
	"time"

	"daimon/internal/config"
)

const (
	// authCookieMaxAge is the cookie Max-Age in seconds (30 days).
	// Used for http.Cookie.MaxAge.
	authCookieMaxAge = 30 * 24 * 60 * 60 // 2592000 seconds

	// authCookieTTL is the same window as a time.Duration, used by the
	// middleware TTL check (time.Since comparison).
	authCookieTTL = time.Duration(authCookieMaxAge) * time.Second
)

// resolveSecureFlag returns true when the Secure cookie flag should be set.
// Rules (FR-7, FR-8, FR-6):
//   - r.TLS != nil  →  true  (native TLS)
//   - TrustProxy=true AND X-Forwarded-Proto: https  →  true
//   - AllowedOrigins non-empty (cross-origin mode)  →  true (SameSite=None requires Secure per spec)
//   - Otherwise  →  false
func resolveSecureFlag(r *http.Request, cfg *config.WebConfig) bool {
	if r.TLS != nil {
		return true
	}
	if cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	if len(cfg.AllowedOrigins) > 0 {
		return true // SameSite=None requires Secure
	}
	return false
}

// resolveSameSite returns SameSite=Strict for same-origin deployments and
// SameSite=None for cross-origin deployments (FR-5, FR-6).
func resolveSameSite(cfg *config.WebConfig) http.SameSite {
	if len(cfg.AllowedOrigins) == 0 {
		return http.SameSiteStrictMode
	}
	return http.SameSiteNoneMode
}

// setAuthCookie writes an HttpOnly cookie carrying the auth token.
// Secure and SameSite flags are resolved from the request and config (FR-4..FR-9).
func setAuthCookie(w http.ResponseWriter, r *http.Request, cfg *config.WebConfig, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: resolveSameSite(cfg),
		Secure:   resolveSecureFlag(r, cfg),
		MaxAge:   authCookieMaxAge,
	})
}

// clearAuthCookie sets an expiring auth cookie (MaxAge=0) to evict the cookie
// from the browser. SameSite and Secure attributes mirror setAuthCookie so
// browsers accept the eviction (FR-18; mismatched attributes can block overwrite).
func clearAuthCookie(w http.ResponseWriter, r *http.Request, cfg *config.WebConfig) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: resolveSameSite(cfg),
		Secure:   resolveSecureFlag(r, cfg),
		MaxAge:   0,
	})
}

// stampIssuedAt sets cfg.AuthTokenIssuedAt = time.Now() and returns the stamped
// value. Callers under rotation MUST be holding configMu. Callers performing
// setup-complete work on a local copy before AtomicWriteConfig.
func stampIssuedAt(cfg *config.WebConfig) time.Time {
	now := time.Now()
	cfg.AuthTokenIssuedAt = now
	return now
}
