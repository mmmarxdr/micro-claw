package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
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
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Static assets and SPA — no auth required.
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}

		// Extract the candidate token.
		candidate := tokenFromRequest(r)

		if !tokenMatch(candidate, token) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// tokenFromRequest extracts the auth token from the request, checking:
//  1. Authorization: Bearer <token> header
//  2. ?token=<token> query parameter (for WebSocket connections)
func tokenFromRequest(r *http.Request) string {
	// Header first.
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			return auth[len(prefix):]
		}
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
