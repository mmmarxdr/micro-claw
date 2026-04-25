package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// loginRequest is the request body for POST /api/auth/login.
type loginRequest struct {
	Token string `json:"token"`
}

// handleLogin handles POST /api/auth/login.
//
// This endpoint is EXEMPT from authMiddlewareDynamic (FR-15): the route is
// registered outside the auth-protected tree in routes().
//
// Flow:
//  1. Decode JSON body.
//  2. Reject with 401 if server token is empty (pre-setup mode — no valid token exists).
//  3. Constant-time compare (FR-11, NFR-1).
//  4. On match: set HttpOnly cookie (FR-12), respond 204 — NO body, NO IssuedAt update (FR-56).
//  5. On mismatch: respond 401, no cookie (FR-13).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg := s.config()
	expected := cfg.Web.AuthToken

	// Pre-setup mode: no token exists, cannot authenticate.
	if expected == "" {
		slog.Warn("login rejected: server has no auth token (pre-setup mode)")
		writeAuthFailure(w)
		return
	}

	if !tokenMatch(req.Token, expected) {
		slog.Warn("login rejected: bad token", "reason", "bad-token")
		writeAuthFailure(w)
		return
	}

	// FR-56: DO NOT update AuthTokenIssuedAt on login — only rotation resets it.
	setAuthCookie(w, r, &cfg.Web, expected)
	slog.Info("login successful")
	w.WriteHeader(http.StatusNoContent)
}
