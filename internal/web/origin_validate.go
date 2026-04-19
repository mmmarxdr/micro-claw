package web

import (
	"net/http"
	"net/url"
	"strings"
)

// validateOrigin checks whether the incoming request's origin is permitted.
//
// Rules (FR-26..FR-29):
//   - If allowedOrigins is empty (same-origin mode): always returns true — no check.
//   - If allowedOrigins is non-empty (cross-origin mode):
//     1. Read the Origin header.
//     2. Fall back to the Referer header's origin component when Origin is absent (FR-27).
//     3. Return true only if the resolved origin is in the allowlist.
//     4. If neither Origin nor Referer is present, return false (FR-28).
func validateOrigin(r *http.Request, allowedOrigins []string) bool {
	// FR-29: same-origin mode — skip entirely.
	if len(allowedOrigins) == 0 {
		return true
	}

	// Build a lookup set for O(1) checks.
	set := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		set[strings.TrimRight(o, "/")] = true
	}

	// FR-26: check Origin header first.
	if origin := r.Header.Get("Origin"); origin != "" {
		return set[strings.TrimRight(origin, "/")]
	}

	// FR-27: fall back to Referer when Origin is absent.
	if referer := r.Header.Get("Referer"); referer != "" {
		u, err := url.Parse(referer)
		if err == nil && u.Host != "" {
			// Reconstruct the origin component (scheme + host, no path).
			refOrigin := u.Scheme + "://" + u.Host
			return set[refOrigin]
		}
	}

	// FR-28: cross-origin mode, no Origin or Referer → reject.
	return false
}

// requireOriginIfCrossOrigin wraps a handler with origin validation.
// In same-origin mode (AllowedOrigins empty), it is a no-op pass-through.
// In cross-origin mode, it validates the Origin (or Referer fallback) and
// returns 403 if the request is not from an allowed origin (FR-28, AS-9).
//
// This wrapper is applied selectively to mutating endpoints (POST/PUT/PATCH/DELETE)
// in server.go's routes() function.
func requireOriginIfCrossOrigin(allowedOrigins []string, next http.Handler) http.Handler {
	// Same-origin mode: no overhead, pass through immediately.
	if len(allowedOrigins) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateOrigin(r, allowedOrigins) {
			writeError(w, http.StatusForbidden, "origin not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}
