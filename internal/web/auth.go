package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"
)

// GenerateToken returns a cryptographically random 32-byte hex token (64 chars).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// authMiddleware rejects requests to /api/* and /ws/* that do not carry a
// valid bearer token. Static file routes (the SPA) are served without auth
// so the frontend can render its own token prompt on 401.
//
// HTTP:      Authorization: Bearer <token>
// WebSocket: ?token=<token> query parameter (browsers cannot set headers on WS).
//
// Deprecated: prefer authMiddlewareDynamic directly with explicit accessor funcs.
func authMiddleware(token string, next http.Handler) http.Handler {
	return authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return time.Time{} }, // no TTL check for static token callers
		next,
	)
}

// authMiddlewareDynamic reads the expected token AND the token issuance time
// from closures on every request (INV-1, INV-8 — never captured at startup).
//
// Flow:
//  1. Static/SPA paths bypass auth entirely.
//  2. /api/setup/* (except /reset) bypass auth (pre-setup accessible).
//  3. If tokenFn() == "" → pre-setup mode, allow all (FR-23, INV-2).
//  4. Extract candidate from request (Bearer > cookie > ?token=).
//  5. Token mismatch → 401; if from cookie, also clear-cookie (AS-4).
//  6. Token match → TTL check via issuedAtFn() (FR-57, FR-58, INV-9).
//     TTL is checked AFTER match so bad tokens cannot probe IssuedAt state (D2b).
//  7. TTL expired → 401 + clear-cookie (AS-22).
//  8. All checks pass → delegate to next.
func authMiddlewareDynamic(tokenFn func() string, issuedAtFn func() time.Time, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Static assets and SPA — no auth required.
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}

		// Setup endpoints are always accessible — no config exists before first run.
		// Exception: /api/setup/reset requires auth (destructive operation).
		if strings.HasPrefix(path, "/api/setup/") && path != "/api/setup/reset" {
			next.ServeHTTP(w, r)
			return
		}

		expected := tokenFn()
		// INV-2: pre-setup bypass — if no token configured, allow all requests.
		if expected == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Extract the candidate token (priority: Bearer > cookie > ?token=).
		candidate := tokenFromRequest(r)

		if !tokenMatch(candidate, expected) {
			// If the invalid token came from a cookie, clear it so the browser
			// does not keep replaying a stale value (AS-4).
			if fromCookie(r) {
				clearStaleAuthCookie(w, r)
			}
			writeAuthFailure(w)
			return
		}

		// TTL check runs AFTER token match (D2b — prevents IssuedAt probing by bad tokens).
		issuedAt := issuedAtFn()
		if !issuedAt.IsZero() && time.Since(issuedAt) > authCookieTTL {
			clearStaleAuthCookie(w, r)
			writeAuthFailure(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// clearStaleAuthCookie clears the "auth" cookie using conservative defaults.
// Used by the middleware where the WebConfig is not available. The MaxAge=0
// eviction is accepted by all browsers regardless of the original SameSite
// value for same-site cookies; cross-origin browsers use explicit handlers.
func clearStaleAuthCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   0,
	})
}

// writeAuthFailure sends a 401 Unauthorized response with a JSON body.
// Using an inline helper instead of http.Error avoids a trailing newline
// difference in tests and keeps the response shape consistent.
func writeAuthFailure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

// fromCookie reports whether the auth token in the request came from the
// "auth" cookie (as opposed to the Authorization header or ?token= param).
func fromCookie(r *http.Request) bool {
	if auth := r.Header.Get("Authorization"); auth != "" {
		return false
	}
	c, err := r.Cookie("auth")
	return err == nil && c.Value != ""
}

// tokenFromRequest extracts the auth token from the request, checking:
//  1. Authorization: Bearer <token> header
//  2. HttpOnly cookie named "auth"
//  3. ?token=<token> query parameter (for WebSocket connections)
func tokenFromRequest(r *http.Request) string {
	// Header first.
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			return auth[len(prefix):]
		}
	}
	// HttpOnly cookie.
	if c, err := r.Cookie("auth"); err == nil && c.Value != "" {
		return c.Value
	}
	// Query param fallback (WebSocket).
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	return ""
}

// tokenMatch performs a constant-time comparison to prevent timing attacks.
func tokenMatch(candidate, expected string) bool {
	if candidate == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1
}
