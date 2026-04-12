package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microagent/internal/audit"
	"microagent/internal/config"
	"microagent/internal/store"
	"microagent/internal/web"
)

// runWebCommand implements `microagent web` — starts ONLY the web dashboard
// (no agent loop, no channel). Blocks until SIGINT/SIGTERM.
func runWebCommand(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	port := fs.Int("port", 0, "override web dashboard port")
	host := fs.String("host", "", "override web dashboard host")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("web: failed to load config: %w", err)
	}

	// Override from flags.
	if *port > 0 {
		cfg.Web.Port = *port
	}
	if *host != "" {
		cfg.Web.Host = *host
	}
	// Force-enable for this subcommand regardless of config setting.
	cfg.Web.Enabled = true

	configureLogging(cfg.Logging)

	st, err := store.New(cfg.Store)
	if err != nil {
		return fmt.Errorf("web: failed to initialize store: %w", err)
	}
	defer st.Close()

	var aud audit.Auditor = audit.NoopAuditor{}
	if cfg.Audit.Enabled {
		switch cfg.Audit.Type {
		case "sqlite":
			sa, err := audit.NewSQLiteAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize sqlite auditor, using noop", "error", err)
			} else {
				aud = sa
				slog.Info("audit enabled", "type", "sqlite", "path", cfg.Audit.Path)
			}
		default:
			fa, err := audit.NewFileAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize file auditor, using noop", "error", err)
			} else {
				aud = fa
				slog.Info("audit enabled", "type", "file", "path", cfg.Audit.Path)
			}
		}
	}

	srv := web.NewServer(web.ServerDeps{
		Store:     st,
		Auditor:   aud,
		Config:    cfg,
		StartedAt: time.Now(),
		Version:   version,
	})

	if err := srv.Start(); err != nil {
		return fmt.Errorf("web: failed to start server: %w", err)
	}
	slog.Info("web dashboard available", "url", fmt.Sprintf("http://%s:%d", cfg.Web.Host, cfg.Web.Port))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("web dashboard shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
