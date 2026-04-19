package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"daimon/internal/content"
	"daimon/internal/provider"
)

// insertMediaBlob inserts a media blob directly into the DB with explicit
// timestamps, bypassing StoreMedia. Used to set up prune test scenarios.
func insertMediaBlob(t *testing.T, db *sql.DB, sha, mime string, data []byte, lastReferencedAt time.Time) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	lra := lastReferencedAt.UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO media_blobs (sha256, mime, size, data, created_at, last_referenced_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sha, mime, int64(len(data)), data, now, lra,
	)
	if err != nil {
		t.Fatalf("insertMediaBlob: %v", err)
	}
}

// ─── StoreMedia ──────────────────────────────────────────────────────────────

func TestSQLiteStore_StoreMedia_ReturnsSHA256(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	data := []byte("hello media world")
	sha, err := s.StoreMedia(ctx, data, "image/jpeg")
	if err != nil {
		t.Fatalf("StoreMedia: %v", err)
	}

	// Verify format: 64-character lowercase hex.
	if len(sha) != 64 {
		t.Errorf("sha256 length: got %d want 64", len(sha))
	}

	want := fmt.Sprintf("%x", sha256.Sum256(data))
	if sha != want {
		t.Errorf("sha256 mismatch: got %q want %q", sha, want)
	}
}

func TestSQLiteStore_StoreMedia_Dedup(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	data := []byte("duplicate content")
	sha1, err := s.StoreMedia(ctx, data, "image/jpeg")
	if err != nil {
		t.Fatalf("first StoreMedia: %v", err)
	}

	sha2, err := s.StoreMedia(ctx, data, "image/jpeg")
	if err != nil {
		t.Fatalf("second StoreMedia: %v", err)
	}

	if sha1 != sha2 {
		t.Errorf("dedup: sha mismatch got %q / %q", sha1, sha2)
	}

	// Exactly one row in the table.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_blobs WHERE sha256 = ?`, sha1).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("dedup: expected 1 row, got %d", count)
	}
}

// ─── GetMedia ────────────────────────────────────────────────────────────────

func TestSQLiteStore_GetMedia_RoundTrip(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a} // PNG header
	mime := "image/png"

	sha, err := s.StoreMedia(ctx, data, mime)
	if err != nil {
		t.Fatalf("StoreMedia: %v", err)
	}

	gotData, gotMime, err := s.GetMedia(ctx, sha)
	if err != nil {
		t.Fatalf("GetMedia: %v", err)
	}

	if gotMime != mime {
		t.Errorf("MIME: got %q want %q", gotMime, mime)
	}
	if string(gotData) != string(data) {
		t.Errorf("data mismatch: got %v want %v", gotData, data)
	}
}

func TestSQLiteStore_GetMedia_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	_, _, err := s.GetMedia(ctx, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != ErrMediaNotFound {
		t.Errorf("expected ErrMediaNotFound, got %v", err)
	}
}

// ─── TouchMedia ──────────────────────────────────────────────────────────────

func TestSQLiteStore_TouchMedia_UpdatesTimestamp(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	data := []byte("touchable")
	sha, err := s.StoreMedia(ctx, data, "text/plain")
	if err != nil {
		t.Fatalf("StoreMedia: %v", err)
	}

	// Read the current last_referenced_at.
	var before string
	if err := s.db.QueryRow(
		`SELECT last_referenced_at FROM media_blobs WHERE sha256 = ?`, sha,
	).Scan(&before); err != nil {
		t.Fatalf("SELECT before: %v", err)
	}

	// Small sleep so RFC3339 timestamp advances by at least one second.
	time.Sleep(1100 * time.Millisecond)

	if err := s.TouchMedia(ctx, sha); err != nil {
		t.Fatalf("TouchMedia: %v", err)
	}

	var after string
	if err := s.db.QueryRow(
		`SELECT last_referenced_at FROM media_blobs WHERE sha256 = ?`, sha,
	).Scan(&after); err != nil {
		t.Fatalf("SELECT after: %v", err)
	}

	if after <= before {
		t.Errorf("last_referenced_at not advanced: before=%q after=%q", before, after)
	}
}

func TestSQLiteStore_TouchMedia_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.TouchMedia(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != ErrMediaNotFound {
		t.Errorf("expected ErrMediaNotFound, got %v", err)
	}
}

// ─── PruneUnreferencedMedia ──────────────────────────────────────────────────

func TestSQLiteStore_PruneUnreferencedMedia(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	stale := now.Add(-48 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	// Blob A: stale + unreferenced → should be deleted.
	shaA := fmt.Sprintf("%x", sha256.Sum256([]byte("blobA")))
	insertMediaBlob(t, s.db, shaA, "image/jpeg", []byte("blobA"), stale)

	// Blob B: fresh → should NOT be deleted even if unreferenced.
	shaB := fmt.Sprintf("%x", sha256.Sum256([]byte("blobB")))
	insertMediaBlob(t, s.db, shaB, "image/jpeg", []byte("blobB"), fresh)

	// Blob C: stale but referenced in a conversation → should NOT be deleted.
	shaC := fmt.Sprintf("%x", sha256.Sum256([]byte("blobC")))
	insertMediaBlob(t, s.db, shaC, "image/jpeg", []byte("blobC"), stale)

	// Save a conversation that references blob C.
	conv := Conversation{
		ID:        "prune-test-conv",
		ChannelID: "cli",
		Messages: []provider.ChatMessage{
			{
				Role: "user",
				Content: content.Blocks{
					{Type: content.BlockImage, MediaSHA256: shaC, MIME: "image/jpeg", Size: 5},
				},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	// Prune blobs older than 24 hours.
	deleted, err := s.PruneUnreferencedMedia(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUnreferencedMedia: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted count: got %d want 1", deleted)
	}

	// Verify A is gone.
	if _, _, err := s.GetMedia(ctx, shaA); err != ErrMediaNotFound {
		t.Errorf("blob A: expected ErrMediaNotFound after prune, got %v", err)
	}

	// Verify B is still there.
	if _, _, err := s.GetMedia(ctx, shaB); err != nil {
		t.Errorf("blob B: unexpected error after prune: %v", err)
	}

	// Verify C is still there.
	if _, _, err := s.GetMedia(ctx, shaC); err != nil {
		t.Errorf("blob C: unexpected error after prune: %v", err)
	}
}
