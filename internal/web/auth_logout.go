package web

import (
	"context"
	"log/slog"
	"net/http"

	"daimon/internal/config"
)

// handleLogout handles POST /api/auth/logout.
//
// This endpoint is BEHIND authMiddlewareDynamic (FR-16): a caller without a
// valid cookie will receive 401 from the middleware before reaching this handler.
//
// Flow:
//  1. Call rotateAuthToken (two-phase write: disk first, memory second, under configMu).
//  2. On rotation error: respond 500 — in-memory state is unchanged (AS-18, INV-3).
//  3. On success: clear the auth cookie (FR-18) and respond 204 (FR-19).
//
// FR-20: a stale/expired cookie causes 401 from the middleware, not from here.
// Idempotency note: logout always rotates, making any pre-existing cookie stale.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	_, err := s.rotateAuthToken()
	if err != nil {
		slog.Error("logout: rotation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "rotation failed")
		return
	}

	// Clear the browser's auth cookie. The new token is already on disk and in
	// memory, so the old cookie is now invalid regardless of whether the browser
	// honours Max-Age=0.
	cfg := s.config()
	clearAuthCookie(w, r, &cfg.Web)
	slog.Info("logout successful: token rotated, cookie cleared")
	w.WriteHeader(http.StatusNoContent)
}

// rotateAuthToken generates a new 256-bit auth token and persists it. It is a
// thin wrapper around rotateAuthTokenWithLogger using the default slog logger.
func (s *Server) rotateAuthToken() (string, error) {
	return s.rotateAuthTokenWithLogger(context.Background(), slog.Default())
}

// rotateAuthTokenWithLogger generates a new 256-bit auth token, persists BOTH the new
// token and a fresh AuthTokenIssuedAt to disk first (INV-3), then updates
// in-memory state (INV-6). The entire operation runs under configMu.
//
// Each phase is logged individually (NFR-3): disk-write-complete, memory-updated,
// issued-at-stamped. On disk failure an error is logged before returning.
//
// On any error from AtomicWriteConfig the in-memory state is left untouched
// so the server remains consistent with the on-disk config (NFR-5, AS-18).
//
// Returns the new token so callers (logout handler, setup-complete) can set
// the cookie without re-reading s.deps.Config.Web.AuthToken.
func (s *Server) rotateAuthTokenWithLogger(_ context.Context, logger *slog.Logger) (string, error) {
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()

	// Build a local copy, mutate BOTH fields, write to disk.
	newCfg := *s.deps.Config
	newCfg.Web.AuthToken = tok
	newNow := stampIssuedAt(&newCfg.Web) // sets newCfg.Web.AuthTokenIssuedAt = time.Now()

	// Phase 1: disk-first (INV-3). On failure, in-memory is UNTOUCHED.
	if err := config.AtomicWriteConfig(s.deps.ConfigPath, &newCfg); err != nil {
		logger.Error("auth: rotation disk-write-failed", "err", err)
		return "", err
	}
	logger.Info("auth: rotation disk-write-complete")

	// Phase 2: in-memory update — both fields, atomically under configMu (INV-6).
	s.deps.Config.Web.AuthToken = tok
	s.deps.Config.Web.AuthTokenIssuedAt = newNow
	logger.Info("auth: rotation memory-updated")
	logger.Info("auth: rotation issued-at-stamped")

	return tok, nil
}
