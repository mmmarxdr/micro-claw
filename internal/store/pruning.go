package store

import (
	"context"
	"fmt"
)

// PruneConfig holds parameters for a single memory pruning cycle.
//
// Lambda and BoostFactor are hardcoded constants (0.03 and 0.5) exposed here
// for documentation and testability — callers should use the defaults from
// DefaultPruneConfig unless they have a specific reason to override them.
//
// Pruning score formula:
//
//	score = exp(-Lambda * age_days) + ln(1 + access_count) * BoostFactor
//
// With the defaults:
//   - Half-life ≈ 23 days (zero accesses).
//   - Reaches the default threshold (0.1) at ≈ 77 days with zero accesses.
//   - 15 accesses adds ≈ 1.39 to the score, extending useful life to ≈ 170 days.
type PruneConfig struct {
	// Threshold is the minimum score required to keep a memory entry.
	// Entries with score < Threshold are soft-deleted (archived_at set).
	// Default: 0.1
	Threshold float64

	// RetentionDays is how many days to keep archived entries before hard-deleting them.
	// Default: 30
	RetentionDays int

	// Lambda is the exponential decay rate. Hardcoded to 0.03.
	// Half-life = ln(2)/Lambda ≈ 23 days.
	Lambda float64

	// BoostFactor scales the access-count contribution to the score.
	// Hardcoded to 0.5.
	BoostFactor float64
}

// DefaultPruneConfig holds the recommended defaults referenced in PruneConfig's doc comment.
// Use this as a starting point and override only the fields you need.
var DefaultPruneConfig = PruneConfig{
	Threshold:     0.1,
	RetentionDays: 30,
	Lambda:        0.03,
	BoostFactor:   0.5,
}

// PruneMemories executes one pruning cycle on the store:
//
//  1. Soft-delete: sets archived_at = now for non-archived entries whose
//     decay score falls below cfg.Threshold.
//  2. Hard-delete: permanently removes entries that have been archived for
//     longer than cfg.RetentionDays days.
//
// All operations run inside a single transaction for atomicity.
//
// Returns the count of newly soft-deleted entries (pruned) and the count of
// permanently removed entries (deleted). Returns an error only for database
// failures; a zero-row result is not an error.
//
// Pruning is SQLite-only. modernc.org/sqlite includes the math extension, so
// exp() and ln() are available in SQL. If those functions ever become
// unavailable, compute scores in Go instead (see design doc).
func (s *SQLiteStore) PruneMemories(ctx context.Context, cfg PruneConfig) (pruned int, deleted int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("pruning: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Step 1 — Soft-delete entries below the score threshold.
	// The score formula is computed in SQL using the built-in exp() and ln()
	// functions provided by modernc.org/sqlite's math extension.
	//
	// substr(created_at,1,19) trims the Go time.Time string representation
	// ("YYYY-MM-DD HH:MM:SS +0000 UTC") to the form julianday() can parse
	// ("YYYY-MM-DD HH:MM:SS").
	softRes, err := tx.ExecContext(ctx, `
		UPDATE memory
		SET archived_at = datetime('now')
		WHERE archived_at IS NULL
		  AND (
		      exp(-(?) * MAX(0.0, julianday('now') - julianday(substr(created_at, 1, 19))))
		      + ln(1.0 + CAST(access_count AS REAL)) * (?)
		  ) < ?`,
		cfg.Lambda, cfg.BoostFactor, cfg.Threshold,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("pruning: soft-delete: %w", err)
	}
	softCount, err := softRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("pruning: rows affected (soft): %w", err)
	}

	// Step 2 — Hard-delete entries that have been archived longer than RetentionDays.
	// The memory_ad trigger fires automatically, removing the corresponding FTS5
	// entries from memory_fts so the search index stays consistent.
	hardRes, err := tx.ExecContext(ctx, `
		DELETE FROM memory
		WHERE archived_at IS NOT NULL
		  AND julianday('now') - julianday(substr(archived_at, 1, 19)) > ?`,
		cfg.RetentionDays,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("pruning: hard-delete: %w", err)
	}
	hardCount, err := hardRes.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("pruning: rows affected (hard): %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("pruning: commit: %w", err)
	}

	return int(softCount), int(hardCount), nil
}
