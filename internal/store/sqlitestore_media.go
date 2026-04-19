package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"daimon/internal/provider"
)

// StoreMedia content-addressably stores data with the given MIME type.
// Computes the SHA-256 digest of data, then INSERT OR IGNORE so identical
// blobs are deduplicated. Returns the lowercase hex sha256 regardless.
func (s *SQLiteStore) StoreMedia(ctx context.Context, data []byte, mime string) (string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO media_blobs (sha256, mime, size, data, created_at, last_referenced_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		digest, mime, int64(len(data)), data, now, now,
	)
	if err != nil {
		return "", fmt.Errorf("storing media blob %s: %w", digest, err)
	}

	return digest, nil
}

// GetMedia retrieves blob bytes and MIME type by lowercase hex sha256 digest.
// Returns ErrMediaNotFound if no row exists for sha256.
func (s *SQLiteStore) GetMedia(ctx context.Context, sha256hex string) ([]byte, string, error) {
	var data []byte
	var mime string

	err := s.db.QueryRowContext(ctx,
		`SELECT data, mime FROM media_blobs WHERE sha256 = ?`, sha256hex,
	).Scan(&data, &mime)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", ErrMediaNotFound
		}
		return nil, "", fmt.Errorf("getting media blob %s: %w", sha256hex, err)
	}

	return data, mime, nil
}

// TouchMedia updates last_referenced_at to now for the given sha256 digest.
// Returns ErrMediaNotFound if no row exists for sha256.
func (s *SQLiteStore) TouchMedia(ctx context.Context, sha256hex string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := s.db.ExecContext(ctx,
		`UPDATE media_blobs SET last_referenced_at = ? WHERE sha256 = ?`, now, sha256hex,
	)
	if err != nil {
		return fmt.Errorf("touching media blob %s: %w", sha256hex, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for touch %s: %w", sha256hex, err)
	}
	if n == 0 {
		return ErrMediaNotFound
	}

	return nil
}

// PruneUnreferencedMedia deletes blobs whose last_referenced_at is older than
// olderThan and that are not referenced by any stored conversation. Returns the
// number of blobs deleted.
//
// Concurrency: the scan and delete run inside a single transaction. Blobs
// written between the SELECT and DELETE are safe — INSERT OR IGNORE for new
// blobs will succeed after the transaction commits, and any missed blob will be
// collected in the next prune run.
func (s *SQLiteStore) PruneUnreferencedMedia(ctx context.Context, olderThan time.Duration) (int, error) {
	threshold := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin prune tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Collect all sha256s referenced in conversation message content blocks.
	// We load and parse in Go (approach b) — simpler and correct at SQLite scale.
	rows, err := tx.QueryContext(ctx, `SELECT messages FROM conversations`)
	if err != nil {
		return 0, fmt.Errorf("querying conversations for prune: %w", err)
	}

	referenced := make(map[string]struct{})
	for rows.Next() {
		var messagesJSON string
		if err := rows.Scan(&messagesJSON); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning messages: %w", err)
		}

		var msgs []provider.ChatMessage
		if err := json.Unmarshal([]byte(messagesJSON), &msgs); err != nil {
			// Tolerate malformed rows — don't abort the prune.
			continue
		}

		for _, msg := range msgs {
			for _, block := range msg.Content {
				if block.MediaSHA256 != "" {
					referenced[block.MediaSHA256] = struct{}{}
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating conversations: %w", err)
	}
	rows.Close()

	// Build the NOT IN clause from the referenced set.
	var res sql.Result
	if len(referenced) == 0 {
		res, err = tx.ExecContext(ctx,
			`DELETE FROM media_blobs WHERE last_referenced_at < ?`, threshold,
		)
	} else {
		shas := make([]string, 0, len(referenced))
		for sha := range referenced {
			shas = append(shas, sha)
		}

		placeholders := strings.Repeat("?,", len(shas))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma

		args := make([]any, 0, 1+len(shas))
		args = append(args, threshold)
		for _, sha := range shas {
			args = append(args, sha)
		}

		res, err = tx.ExecContext(ctx,
			`DELETE FROM media_blobs WHERE last_referenced_at < ? AND sha256 NOT IN (`+placeholders+`)`,
			args...,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("deleting stale media blobs: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected for prune: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing prune tx: %w", err)
	}

	return int(n), nil
}

// ListMedia returns metadata for all stored blobs, ordered by creation time
// descending (newest first). The blob data itself is NOT returned.
func (s *SQLiteStore) ListMedia(ctx context.Context) ([]MediaMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sha256, mime, size, created_at, last_referenced_at
		 FROM media_blobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing media blobs: %w", err)
	}
	defer rows.Close()

	var result []MediaMeta
	for rows.Next() {
		var m MediaMeta
		if err := rows.Scan(&m.SHA256, &m.MIME, &m.Size, &m.CreatedAt, &m.LastReferencedAt); err != nil {
			return nil, fmt.Errorf("scanning media row: %w", err)
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating media rows: %w", err)
	}
	return result, nil
}

// DeleteMedia removes a blob by its SHA-256 hex digest.
// Returns ErrMediaNotFound if the digest is unknown.
func (s *SQLiteStore) DeleteMedia(ctx context.Context, sha256hex string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM media_blobs WHERE sha256 = ?`, sha256hex)
	if err != nil {
		return fmt.Errorf("deleting media blob %s: %w", sha256hex, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for delete %s: %w", sha256hex, err)
	}
	if n == 0 {
		return ErrMediaNotFound
	}
	return nil
}

// touchMediaBatch updates last_referenced_at for a slice of sha256 digests in
// a single UPDATE ... WHERE sha256 IN (...) statement. Missing sha256s are
// silently ignored (no ErrMediaNotFound — this is a best-effort batch path).
// If shas is empty, this is a no-op.
func (s *SQLiteStore) touchMediaBatch(ctx context.Context, shas []string) error {
	if len(shas) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	placeholders := strings.Repeat("?,", len(shas))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, 0, 1+len(shas))
	args = append(args, now)
	for _, sha := range shas {
		args = append(args, sha)
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE media_blobs SET last_referenced_at = ? WHERE sha256 IN (`+placeholders+`)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("batch touch media: %w", err)
	}

	return nil
}

// collectMediaSHAs walks a slice of ChatMessages and returns the distinct
// sha256 digests found in all media content blocks.
func collectMediaSHAs(msgs []provider.ChatMessage) []string {
	seen := make(map[string]struct{})
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.MediaSHA256 != "" {
				seen[block.MediaSHA256] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for sha := range seen {
		out = append(out, sha)
	}
	return out
}

// Compile-time assertion: *SQLiteStore implements MediaStore.
var _ MediaStore = (*SQLiteStore)(nil)
