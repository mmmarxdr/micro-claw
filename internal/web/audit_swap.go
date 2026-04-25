package web

import (
	"log/slog"

	"daimon/internal/audit"
	"daimon/internal/config"
)

// CurrentAuditor returns the running audit backend under a read lock so
// concurrent callers can read it safely while a config PUT hot-swaps it.
// It is exported so cmd/daimon/web_cmd.go can pass it as an accessor to
// agent.Agent.WithAuditorAccessor, eliminating the stale-auditor race.
func (s *Server) CurrentAuditor() audit.Auditor {
	s.auditorMu.RLock()
	defer s.auditorMu.RUnlock()
	return s.deps.Auditor
}

// rebuildAuditor constructs a new audit backend from cfg and atomically
// replaces the running one, closing the old auditor afterwards. Called
// from handlePutConfig when the audit subtree is patched so users do not
// need to restart for the change to take effect.
//
// Selection logic mirrors cmd/daimon/web_cmd.go and main.go: respects
// Audit.Enabled (*bool, defaults true via ApplyDefaults), then dispatches
// on Audit.Type (sqlite|file). Initialization errors fall back to noop
// so a misconfigured path never crashes the server.
func (s *Server) rebuildAuditor(cfg *config.Config) {
	var newAud audit.Auditor = audit.NoopAuditor{}

	if config.BoolVal(cfg.Audit.Enabled) {
		switch cfg.Audit.Type {
		case "sqlite":
			sa, err := audit.NewSQLiteAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("audit hot-swap: sqlite init failed, using noop", "error", err, "path", cfg.Audit.Path)
			} else {
				newAud = sa
				slog.Info("audit hot-swapped", "type", "sqlite", "path", cfg.Audit.Path)
			}
		default:
			fa, err := audit.NewFileAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("audit hot-swap: file init failed, using noop", "error", err, "path", cfg.Audit.Path)
			} else {
				newAud = fa
				slog.Info("audit hot-swapped", "type", "file", "path", cfg.Audit.Path)
			}
		}
	} else {
		slog.Info("audit hot-swapped", "type", "noop")
	}

	s.auditorMu.Lock()
	old := s.deps.Auditor
	s.deps.Auditor = newAud
	s.auditorMu.Unlock()

	if old != nil {
		if err := old.Close(); err != nil {
			slog.Warn("audit hot-swap: closing old auditor failed", "error", err)
		}
	}
}
