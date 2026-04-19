package store

import (
	"fmt"

	"daimon/internal/config"
)

// New constructs and returns the Store implementation configured by cfg.Type.
//
//	"file" or "" → FileStore (never errors)
//	"sqlite"     → SQLiteStore (may return an error if the DB cannot be opened)
//	anything else → error
func New(cfg config.StoreConfig) (Store, error) {
	switch cfg.Type {
	case "file", "":
		return NewFileStore(cfg), nil
	case "sqlite":
		return NewSQLiteStore(cfg)
	default:
		return nil, fmt.Errorf("unknown store type: %q", cfg.Type)
	}
}
