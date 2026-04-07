package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"microagent/internal/config"
)

// openPruningTestDB creates an in-memory SQLite database with the full schema
// (migrations v1 → v3) applied. Returns the SQLiteStore and a cleanup func.
func openPruningTestDB(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := NewSQLiteStore(config.StoreConfig{
		Type: "sqlite",
		Path: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// insertMemoryAt inserts a memory entry with a specific created_at and
// optional archived_at, bypassing AppendMemory so we can set arbitrary
// timestamps for testing pruning behaviour.
func insertMemoryAt(t *testing.T, db *sql.DB, id, scopeID, content string, createdAt time.Time, archivedAt *time.Time, accessCount int) {
	t.Helper()
	var archived *string
	if archivedAt != nil {
		s := archivedAt.UTC().Format("2006-01-02 15:04:05")
		archived = &s
	}
	created := createdAt.UTC().Format("2006-01-02 15:04:05")
	_, err := db.Exec(
		`INSERT INTO memory (id, scope_id, topic, type, title, content, tags, source, created_at, access_count, archived_at)
		 VALUES (?, ?, '', '', '', ?, '[]', 'test', ?, ?, ?)`,
		id, scopeID, content, created, accessCount, archived,
	)
	if err != nil {
		t.Fatalf("insertMemoryAt(%s): %v", id, err)
	}
}

// defaultPruneConfig returns a PruneConfig suitable for most pruning tests.
func defaultPruneConfig() PruneConfig {
	return PruneConfig{
		Threshold:     0.1,
		RetentionDays: 30,
		Lambda:        0.03,
		BoostFactor:   0.5,
	}
}

// ---------------------------------------------------------------------------
// 5.1 Test cases
// ---------------------------------------------------------------------------

// TestPruneMemories_SoftDelete_ZeroAccess verifies that a memory entry with
// access_count=0 and age=90 days gets soft-deleted (archived_at set).
//
// Score = exp(-0.03*90) + ln(1+0)*0.5 ≈ 0.067 < 0.1 threshold → archived.
func TestPruneMemories_SoftDelete_ZeroAccess(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -90)
	insertMemoryAt(t, st.db, "e1", "scope1", "old memory", old, nil, 0)

	pruned, deleted, err := st.PruneMemories(ctx, defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories: %v", err)
	}
	if pruned != 1 {
		t.Errorf("expected 1 soft-deleted, got %d", pruned)
	}
	if deleted != 0 {
		t.Errorf("expected 0 hard-deleted, got %d", deleted)
	}

	// Verify archived_at is now set.
	var archivedAt *time.Time
	err = st.db.QueryRowContext(ctx, `SELECT archived_at FROM memory WHERE id = 'e1'`).Scan(&archivedAt)
	if err != nil {
		t.Fatalf("checking archived_at: %v", err)
	}
	if archivedAt == nil {
		t.Error("expected archived_at to be set after soft-delete, got nil")
	}
}

// TestPruneMemories_Retain_HighAccess verifies that a memory with
// access_count=15 and age=90 days is retained — access boost keeps score above threshold.
//
// Score = exp(-0.03*90) + ln(1+15)*0.5 ≈ 0.067 + 1.39 ≈ 1.46 > 0.1 threshold → kept.
func TestPruneMemories_Retain_HighAccess(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -90)
	insertMemoryAt(t, st.db, "e2", "scope1", "frequently accessed memory", old, nil, 15)

	pruned, deleted, err := st.PruneMemories(ctx, defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories: %v", err)
	}
	if pruned != 0 {
		t.Errorf("expected 0 soft-deleted for high-access entry, got %d", pruned)
	}
	if deleted != 0 {
		t.Errorf("expected 0 hard-deleted, got %d", deleted)
	}

	// Verify entry is still present and not archived.
	var archivedAt *time.Time
	err = st.db.QueryRowContext(ctx, `SELECT archived_at FROM memory WHERE id = 'e2'`).Scan(&archivedAt)
	if err != nil {
		t.Fatalf("checking archived_at: %v", err)
	}
	if archivedAt != nil {
		t.Errorf("expected archived_at to remain nil for high-access entry, got %v", archivedAt)
	}
}

// TestPruneMemories_HardDelete_OldArchive verifies that an entry archived 35 days
// ago with retention=30 is hard-deleted.
func TestPruneMemories_HardDelete_OldArchive(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	created := time.Now().AddDate(0, 0, -120)
	archived := time.Now().AddDate(0, 0, -35)
	insertMemoryAt(t, st.db, "e3", "scope1", "old archived memory", created, &archived, 0)

	pruned, deleted, err := st.PruneMemories(ctx, defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 hard-deleted, got %d", deleted)
	}
	// pruned may be 0 since it was already archived
	_ = pruned

	// Verify the row is gone.
	var count int
	err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory WHERE id = 'e3'`).Scan(&count)
	if err != nil {
		t.Fatalf("checking deletion: %v", err)
	}
	if count != 0 {
		t.Error("expected entry to be hard-deleted from memory table")
	}
}

// TestPruneMemories_Retain_RecentArchive verifies that an entry archived 10 days
// ago with retention=30 is NOT hard-deleted.
func TestPruneMemories_Retain_RecentArchive(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	created := time.Now().AddDate(0, 0, -50)
	archived := time.Now().AddDate(0, 0, -10)
	insertMemoryAt(t, st.db, "e4", "scope1", "recently archived memory", created, &archived, 0)

	pruned, deleted, err := st.PruneMemories(ctx, defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 hard-deleted for recently archived entry, got %d", deleted)
	}
	_ = pruned

	// Verify the row is still present.
	var count int
	err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory WHERE id = 'e4'`).Scan(&count)
	if err != nil {
		t.Fatalf("checking retention: %v", err)
	}
	if count != 1 {
		t.Error("expected recently archived entry to be retained")
	}
}

// TestPruneMemories_SearchExcludesArchived verifies that SearchMemory does not
// return soft-deleted (archived) entries after PruneMemories runs.
func TestPruneMemories_SearchExcludesArchived(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -90)
	insertMemoryAt(t, st.db, "e5", "scope1", "prunable content", old, nil, 0)

	// Also insert a fresh entry that should survive.
	insertMemoryAt(t, st.db, "e6", "scope1", "fresh content", time.Now(), nil, 0)

	_, _, err := st.PruneMemories(ctx, defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories: %v", err)
	}

	// SearchMemory should not return the pruned entry.
	entries, err := st.SearchMemory(ctx, "scope1", "", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	for _, e := range entries {
		if e.ID == "e5" {
			t.Error("SearchMemory returned soft-deleted entry e5")
		}
	}
	// Fresh entry should still appear.
	found := false
	for _, e := range entries {
		if e.ID == "e6" {
			found = true
		}
	}
	if !found {
		t.Error("SearchMemory should return the fresh entry e6")
	}
}

// TestPruneMemories_Concurrent verifies that concurrent calls to PruneMemories
// do not panic. SQLite serializes writers; some calls may return SQLITE_BUSY,
// which is an expected, non-panic error under concurrent write pressure.
func TestPruneMemories_Concurrent(t *testing.T) {
	st := openPruningTestDB(t)
	ctx := context.Background()

	// Insert a handful of prunable entries.
	for i := 0; i < 10; i++ {
		old := time.Now().AddDate(0, 0, -90)
		id := "ce" + string(rune('0'+i))
		insertMemoryAt(t, st.db, id, "scope1", "old entry", old, nil, 0)
	}

	cfg := defaultPruneConfig()
	var wg sync.WaitGroup
	panicked := make(chan struct{}, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					panicked <- struct{}{}
				}
				wg.Done()
			}()
			// Errors (e.g. SQLITE_BUSY from concurrent writers) are acceptable;
			// panics are not.
			_, _, _ = st.PruneMemories(ctx, cfg)
		}()
	}
	wg.Wait()
	close(panicked)
	for range panicked {
		t.Error("PruneMemories panicked under concurrent access")
	}
}

// TestPruneMemories_EmptyStore verifies that PruneMemories on an empty store
// returns (0, 0, nil) without error.
func TestPruneMemories_EmptyStore(t *testing.T) {
	st := openPruningTestDB(t)
	pruned, deleted, err := st.PruneMemories(context.Background(), defaultPruneConfig())
	if err != nil {
		t.Fatalf("PruneMemories on empty store: %v", err)
	}
	if pruned != 0 || deleted != 0 {
		t.Errorf("expected (0,0) on empty store, got (%d,%d)", pruned, deleted)
	}
}
