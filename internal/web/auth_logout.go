package web

import (
	"microagent/internal/config"
)

// rotateAuthToken generates a new 256-bit auth token, persists BOTH the new
// token and a fresh AuthTokenIssuedAt to disk first (INV-3), then updates
// in-memory state (INV-6). The entire operation runs under configMu.
//
// On any error from AtomicWriteConfig the in-memory state is left untouched
// so the server remains consistent with the on-disk config (NFR-5, AS-18).
//
// Returns the new token so callers (logout handler, setup-complete) can set
// the cookie without re-reading s.deps.Config.Web.AuthToken.
func (s *Server) rotateAuthToken() (string, error) {
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
		return "", err
	}

	// Phase 2: in-memory update — both fields, atomically under configMu (INV-6).
	s.deps.Config.Web.AuthToken = tok
	s.deps.Config.Web.AuthTokenIssuedAt = newNow

	return tok, nil
}
