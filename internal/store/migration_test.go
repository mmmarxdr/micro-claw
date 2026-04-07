package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"microagent/internal/config"
)

// newTestSQLiteStoreRaw opens a SQLiteStore backed by a fresh temp dir.
// Same as newTestSQLiteStore but does not register cleanup (useful when the
// caller wants to reuse the dir across instances).
func openSQLiteStoreAt(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	return s
}

// ─── schema_version seeding ───────────────────────────────────────────────────

// TestMigration_FreshDBHasSchemaVersion verifies that a brand-new database has
// schema_version seeded to the latest version after initSchema.
func TestMigration_FreshDBHasSchemaVersion(t *testing.T) {
	s := newTestSQLiteStore(t)

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	// Fresh DB goes through all migrations → latest version is 3.
	if version != 3 {
		t.Errorf("expected schema_version=3 on fresh DB, got %d", version)
	}
}

// TestMigration_RerunIsNoOp verifies that calling initSchema a second time on
// an already-migrated database is safe and leaves schema_version unchanged.
func TestMigration_RerunIsNoOp(t *testing.T) {
	path := t.TempDir()
	s := openSQLiteStoreAt(t, path)

	// Run migrations a second time by calling initSchema directly.
	if err := s.initSchema(); err != nil {
		t.Fatalf("second initSchema: %v", err)
	}

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 3 {
		t.Errorf("expected schema_version=3 after re-run, got %d", version)
	}
	s.Close()
}

// TestMigration_V1ToV2_UpgradesCleanly simulates an existing v1 database
// (original schema, no schema_version, no new columns) and verifies that
// opening it with the new code upgrades it to v2 cleanly.
func TestMigration_V1ToV2_UpgradesCleanly(t *testing.T) {
	path := t.TempDir()

	// Build a v1-like database manually: apply old schema (no schema_version),
	// insert a row, then open through NewSQLiteStore.
	db, err := sql.Open("sqlite", path+"/microagent.db")
	if err != nil {
		t.Fatalf("opening raw db: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		t.Fatalf("WAL: %v", err)
	}

	// Apply the original v1 schema (no schema_version, no porter tokenizer).
	const v1Schema = `
CREATE TABLE IF NOT EXISTS conversations (
	id         TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	messages   TEXT NOT NULL,
	metadata   TEXT,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS memory (
	id         TEXT PRIMARY KEY,
	scope_id   TEXT NOT NULL,
	topic      TEXT,
	type       TEXT,
	title      TEXT,
	content    TEXT NOT NULL,
	tags       TEXT,
	source     TEXT,
	created_at DATETIME NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
	content,
	tags,
	content='memory',
	content_rowid='rowid'
);
CREATE TRIGGER IF NOT EXISTS memory_ai
	AFTER INSERT ON memory BEGIN
		INSERT INTO memory_fts(rowid, content, tags)
		VALUES (new.rowid, new.content, new.tags);
	END;
CREATE TRIGGER IF NOT EXISTS memory_ad
	AFTER DELETE ON memory BEGIN
		INSERT INTO memory_fts(memory_fts, rowid, content, tags)
		VALUES ('delete', old.rowid, old.content, old.tags);
	END;
CREATE TABLE IF NOT EXISTS secrets (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS cron_jobs (
	id             TEXT PRIMARY KEY,
	schedule       TEXT NOT NULL,
	schedule_human TEXT NOT NULL,
	prompt         TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	enabled        INTEGER NOT NULL DEFAULT 1,
	created_at     INTEGER NOT NULL,
	last_run_at    INTEGER,
	next_run_at    INTEGER
);
CREATE TABLE IF NOT EXISTS cron_results (
	id        TEXT PRIMARY KEY,
	job_id    TEXT NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
	ran_at    INTEGER NOT NULL,
	output    TEXT,
	error_msg TEXT
);
`
	if _, err := db.Exec(v1Schema); err != nil {
		db.Close()
		t.Fatalf("applying v1 schema: %v", err)
	}

	// Insert a memory row before migration.
	if _, err := db.Exec(
		`INSERT INTO memory (id, scope_id, content, created_at) VALUES ('pre-existing', 'scope1', 'authentication token', ?)`,
		time.Now().UTC(),
	); err != nil {
		db.Close()
		t.Fatalf("inserting pre-existing row: %v", err)
	}
	db.Close()

	// Now open through NewSQLiteStore — migration must run cleanly.
	s, err := NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore on v1 db: %v", err)
	}
	defer s.Close()

	// Verify schema_version is 3.
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 3 {
		t.Errorf("expected schema_version=3 after v1→v2→v3, got %d", version)
	}

	// Verify the pre-existing row survived.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory WHERE id = 'pre-existing'`).Scan(&count); err != nil {
		t.Fatalf("counting pre-existing rows: %v", err)
	}
	if count != 1 {
		t.Errorf("pre-existing row was lost during migration, count=%d", count)
	}
}

// ─── v2 migration specifics ───────────────────────────────────────────────────

// TestMigration_V2_NewColumnsExist verifies that the v2 migration added the
// access_count, last_accessed_at, and archived_at columns to the memory table.
func TestMigration_V2_NewColumnsExist(t *testing.T) {
	s := newTestSQLiteStore(t)

	// A SELECT that references the new columns must not error.
	rows, err := s.db.Query(`SELECT access_count, last_accessed_at, archived_at FROM memory WHERE 1=0`)
	if err != nil {
		t.Fatalf("new columns do not exist: %v", err)
	}
	rows.Close()
}

// TestMigration_V2_FTSUsesPorterTokenizer verifies that the migrated memory_fts
// table uses the porter tokenizer by checking sqlite_master DDL.
func TestMigration_V2_FTSUsesPorterTokenizer(t *testing.T) {
	s := newTestSQLiteStore(t)

	var ddl string
	err := s.db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'memory_fts'`,
	).Scan(&ddl)
	if err != nil {
		t.Fatalf("querying sqlite_master for memory_fts: %v", err)
	}

	if !strings.Contains(ddl, "porter") {
		t.Errorf("memory_fts DDL does not contain 'porter' tokenizer: %q", ddl)
	}
}

// TestMigration_V2_PorterStemmerWorks verifies that inserting "authenticated"
// and FTS-searching "authenticate" returns a match (porter stemmer active).
func TestMigration_V2_PorterStemmerWorks(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "porter-test",
		Content:   "the user authenticated successfully",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.AppendMemory(ctx, "scope1", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Search for stem "authenticate" — should match "authenticated".
	results, err := s.SearchMemory(ctx, "scope1", "authenticate", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected porter stemmer to match 'authenticated' when searching 'authenticate', got 0 results")
	}
}

// TestMigration_V2_MemoryAUTriggerExists verifies that the memory_au trigger
// was created by the v2 migration.
func TestMigration_V2_MemoryAUTriggerExists(t *testing.T) {
	s := newTestSQLiteStore(t)

	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = 'memory_au'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("memory_au trigger not found in sqlite_master: %v", err)
	}
	if name != "memory_au" {
		t.Errorf("expected trigger name 'memory_au', got %q", name)
	}
}

// ─── v3 migration specifics ───────────────────────────────────────────────────

// TestMigration_V3_EmbeddingColumnExists verifies that the v3 migration added
// the embedding BLOB column to the memory table.
func TestMigration_V3_EmbeddingColumnExists(t *testing.T) {
	s := newTestSQLiteStore(t)

	rows, err := s.db.Query(`SELECT embedding FROM memory WHERE 1=0`)
	if err != nil {
		t.Fatalf("embedding column does not exist: %v", err)
	}
	rows.Close()
}

// TestMigration_V3_SchemaVersionIs3 verifies final schema_version after all
// migrations complete.
func TestMigration_V3_SchemaVersionIs3(t *testing.T) {
	s := newTestSQLiteStore(t)

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 3 {
		t.Errorf("expected schema_version=3, got %d", version)
	}
}

// ─── migration timing ─────────────────────────────────────────────────────────

// TestMigration_CompletesWithin2Seconds verifies that the entire migration
// (including FTS5 shadow-swap) completes within 2 seconds on an empty store.
func TestMigration_CompletesWithin2Seconds(t *testing.T) {
	start := time.Now()
	s := newTestSQLiteStore(t)
	elapsed := time.Since(start)

	// Confirm migration ran (i.e., schema_version exists).
	var version int
	_ = s.db.QueryRow("SELECT version FROM schema_version").Scan(&version)

	if elapsed > 2*time.Second {
		t.Errorf("migration took %v, expected < 2s", elapsed)
	}
}
