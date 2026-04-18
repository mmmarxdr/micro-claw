package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// v1ProviderRe matches a top-level "provider:" key in YAML.
// The (?m) flag makes ^ match line-start so we don't false-positive on
// indented fields like "  provider:" inside a nested block.
var v1ProviderRe = regexp.MustCompile(`(?m)^provider:\s*($|[\s#])`)

// AtomicWriteConfig marshals cfg to YAML and writes it atomically using a
// temp file in the same directory followed by os.Rename.
//
// Before the rename, if the existing file at path is detected as a v1 config
// (has a top-level "provider:" key) AND no .v1.bak already exists, the
// existing file is copied to path+".v1.bak" as a one-time safety net.
// The backup is best-effort: if it fails, a WARN is logged and the save
// continues normally.
func AtomicWriteConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// ── best-effort v1 backup ───────────────────────────────────────────────
	bakPath := path + ".v1.bak"
	if existing, readErr := os.ReadFile(path); readErr == nil {
		// File exists. Check if it looks like v1 AND backup doesn't exist yet.
		if v1ProviderRe.Match(existing) {
			if _, statErr := os.Stat(bakPath); os.IsNotExist(statErr) {
				if writeErr := os.WriteFile(bakPath, existing, 0o600); writeErr != nil {
					slog.Warn("config: failed to create v1 backup; continuing with save",
						"backup_path", bakPath,
						"error", writeErr)
				} else {
					slog.Info("config: v1 backup created", "backup_path", bakPath)
				}
			}
		}
	}
	// ── atomic write ────────────────────────────────────────────────────────

	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()        //nolint:errcheck
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName) //nolint:errcheck
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
