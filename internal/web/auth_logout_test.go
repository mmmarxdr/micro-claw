package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newServerForRotation creates a minimal Server with configMu available.
// The ConfigPath is set to a temp file so AtomicWriteConfig can write.
func newServerForRotation(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Write a minimal YAML so AtomicWriteConfig can load it.
	initial := []byte("web:\n  auth_token: \"initial-token\"\n  port: 8080\n")
	if err := os.WriteFile(cfgPath, initial, 0o600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	cfg := minimalConfig()
	cfg.Web.AuthToken = "initial-token"
	cfg.Web.AuthTokenIssuedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	s := &Server{
		deps: ServerDeps{
			Config:     cfg,
			ConfigPath: cfgPath,
		},
	}
	return s, cfgPath
}

// TestRotateAuthToken_DiskFailure_LeavesBothFieldsUntouched verifies INV-3/INV-6/AS-18/FR-46/FR-59:
// when AtomicWriteConfig fails, both AuthToken and AuthTokenIssuedAt in memory
// must remain at their pre-rotation values.
func TestRotateAuthToken_DiskFailure_LeavesBothFieldsUntouched(t *testing.T) {
	s, _ := newServerForRotation(t)

	originalToken := s.deps.Config.Web.AuthToken
	originalIssuedAt := s.deps.Config.Web.AuthTokenIssuedAt

	// Make the config path point to a non-writable directory to force disk failure.
	s.deps.ConfigPath = "/non-existent-dir/config.yaml"

	tok, err := s.rotateAuthToken()
	if err == nil {
		t.Fatal("rotateAuthToken: expected error on disk failure, got nil")
	}
	if tok != "" {
		t.Errorf("rotateAuthToken: expected empty token on failure, got %q", tok)
	}
	if s.deps.Config.Web.AuthToken != originalToken {
		t.Errorf("AuthToken mutated on disk failure: got %q, want %q",
			s.deps.Config.Web.AuthToken, originalToken)
	}
	if !s.deps.Config.Web.AuthTokenIssuedAt.Equal(originalIssuedAt) {
		t.Errorf("AuthTokenIssuedAt mutated on disk failure: got %v, want %v",
			s.deps.Config.Web.AuthTokenIssuedAt, originalIssuedAt)
	}
}

// TestRotateAuthToken_Success_StampsNewIssuedAt verifies FR-59/AS-24:
// a successful rotation generates a new token AND stamps a new AuthTokenIssuedAt.
func TestRotateAuthToken_Success_StampsNewIssuedAt(t *testing.T) {
	s, _ := newServerForRotation(t)

	originalToken := s.deps.Config.Web.AuthToken
	originalIssuedAt := s.deps.Config.Web.AuthTokenIssuedAt

	before := time.Now()
	tok, err := s.rotateAuthToken()
	after := time.Now()

	if err != nil {
		t.Fatalf("rotateAuthToken: unexpected error: %v", err)
	}
	if tok == "" {
		t.Fatal("rotateAuthToken: returned empty token on success")
	}
	if tok == originalToken {
		t.Error("rotateAuthToken: new token must differ from original")
	}
	if s.deps.Config.Web.AuthToken != tok {
		t.Errorf("in-memory AuthToken not updated: got %q, want %q",
			s.deps.Config.Web.AuthToken, tok)
	}
	if s.deps.Config.Web.AuthTokenIssuedAt.Equal(originalIssuedAt) {
		t.Error("AuthTokenIssuedAt was not updated after successful rotation")
	}
	if s.deps.Config.Web.AuthTokenIssuedAt.Before(before) || s.deps.Config.Web.AuthTokenIssuedAt.After(after) {
		t.Errorf("AuthTokenIssuedAt %v not in rotation window [%v, %v]",
			s.deps.Config.Web.AuthTokenIssuedAt, before, after)
	}
}
