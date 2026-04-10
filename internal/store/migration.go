package store

import (
	"fmt"
)

// baseSchema contains CREATE TABLE IF NOT EXISTS statements for all non-FTS tables.
// FTS tables and triggers are intentionally EXCLUDED here — they are managed by
// the versioned migration system (migrateV2). This makes the base schema
// idempotent and safe to re-run on any version of the database.
const baseSchema = `
CREATE TABLE IF NOT EXISTS conversations (
	id         TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	messages   TEXT NOT NULL,
	metadata   TEXT,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_channel
	ON conversations(channel_id, updated_at DESC);

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
CREATE INDEX IF NOT EXISTS idx_cron_results_job_ran ON cron_results(job_id, ran_at DESC);
`

// schemaVersionDDL creates the schema_version table and seeds it to version 1
// if it does not already exist. Version 1 represents the original schema
// (before any versioned migrations were added).
const schemaVersionDDL = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
-- Seed with version 1 if the table is empty (covers existing v1 databases).
INSERT INTO schema_version (version)
SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version);
`

// initSchemaVersioned creates the base tables if they do not exist, ensures the
// schema_version table is present, then runs any pending versioned migrations
// in order. It is idempotent — safe to call multiple times on the same database.
func (s *SQLiteStore) initSchemaVersioned() error {
	// 1. Apply base schema (all CREATE TABLE IF NOT EXISTS — idempotent).
	if _, err := s.db.Exec(baseSchema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}

	// 2. Ensure schema_version exists and is seeded to 1 for existing databases.
	if _, err := s.db.Exec(schemaVersionDDL); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}

	// 3. Read the current schema version.
	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	// 4. Run migrations in ascending order, guarded by version checks.
	if version < 2 {
		if err := s.migrateV2(); err != nil {
			return fmt.Errorf("migration v2: %w", err)
		}
	}
	if version < 3 {
		if err := s.migrateV3(); err != nil {
			return fmt.Errorf("migration v3: %w", err)
		}
	}
	if version < 4 {
		if err := s.migrateV4(); err != nil {
			return fmt.Errorf("migration v4: %w", err)
		}
	}

	return nil
}

// migrateV2 performs the Layer 1 migration:
//   - Adds access_count, last_accessed_at, archived_at columns to memory.
//   - Replaces the memory_fts virtual table with a Porter stemmer variant using
//     a shadow-table atomic swap strategy.
//   - Adds the memory_au (after-update) trigger.
//   - Updates schema_version to 2.
//
// All steps run inside a single transaction for atomicity.
func (s *SQLiteStore) migrateV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Add pruning columns to the memory base table.
	// ALTER TABLE … ADD COLUMN is safe to attempt; if the column already exists
	// the migration would have been skipped by the version check, but guard
	// defensively by ignoring "duplicate column" errors only if needed.
	alterStmts := []string{
		`ALTER TABLE memory ADD COLUMN access_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE memory ADD COLUMN last_accessed_at DATETIME`,
		`ALTER TABLE memory ADD COLUMN archived_at DATETIME`,
	}
	for _, stmt := range alterStmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("alter memory table: %w", err)
		}
	}

	// 2. Create a new FTS5 virtual table with the Porter stemmer tokenizer.
	//    Using a shadow name so we can atomically swap it with the old table.
	if _, err := tx.Exec(`
		CREATE VIRTUAL TABLE memory_fts_new USING fts5(
			content,
			tags,
			content='memory',
			content_rowid='rowid',
			tokenize="porter unicode61"
		)
	`); err != nil {
		return fmt.Errorf("creating memory_fts_new: %w", err)
	}

	// 3. Populate the new FTS table from the base table.
	if _, err := tx.Exec(`
		INSERT INTO memory_fts_new(rowid, content, tags)
		SELECT rowid, content, tags FROM memory
	`); err != nil {
		return fmt.Errorf("populating memory_fts_new: %w", err)
	}

	// 4. Drop old triggers (must happen before dropping the FTS table they reference).
	for _, trigger := range []string{"memory_ai", "memory_ad", "memory_au"} {
		if _, err := tx.Exec("DROP TRIGGER IF EXISTS " + trigger); err != nil {
			return fmt.Errorf("dropping trigger %s: %w", trigger, err)
		}
	}

	// 5. Drop the old FTS table.
	if _, err := tx.Exec("DROP TABLE IF EXISTS memory_fts"); err != nil {
		return fmt.Errorf("dropping old memory_fts: %w", err)
	}

	// 6. Rename the new FTS table to the canonical name.
	if _, err := tx.Exec("ALTER TABLE memory_fts_new RENAME TO memory_fts"); err != nil {
		return fmt.Errorf("renaming memory_fts_new: %w", err)
	}

	// 7. Recreate triggers for INSERT, DELETE, and UPDATE.
	triggers := []string{
		`CREATE TRIGGER memory_ai
			AFTER INSERT ON memory BEGIN
				INSERT INTO memory_fts(rowid, content, tags)
				VALUES (new.rowid, new.content, new.tags);
			END`,
		`CREATE TRIGGER memory_ad
			AFTER DELETE ON memory BEGIN
				INSERT INTO memory_fts(memory_fts, rowid, content, tags)
				VALUES ('delete', old.rowid, old.content, old.tags);
			END`,
		`CREATE TRIGGER memory_au
			AFTER UPDATE OF content, tags ON memory BEGIN
				INSERT INTO memory_fts(memory_fts, rowid, content, tags)
				VALUES ('delete', old.rowid, old.content, old.tags);
				INSERT INTO memory_fts(rowid, content, tags)
				VALUES (new.rowid, new.content, new.tags);
			END`,
	}
	for _, trigger := range triggers {
		if _, err := tx.Exec(trigger); err != nil {
			return fmt.Errorf("creating trigger: %w", err)
		}
	}

	// 8. Update schema version.
	if _, err := tx.Exec("UPDATE schema_version SET version = 2"); err != nil {
		return fmt.Errorf("updating schema version to 2: %w", err)
	}

	return tx.Commit()
}

// migrateV3 adds the optional embedding BLOB column to the memory table and
// advances schema_version to 3. This column stores 256-dimensional float32
// vectors serialized as little-endian binary (1,024 bytes). Rows without
// embeddings have NULL in this column, which is valid.
func (s *SQLiteStore) migrateV3() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`ALTER TABLE memory ADD COLUMN embedding BLOB`); err != nil {
		return fmt.Errorf("adding embedding column: %w", err)
	}

	if _, err := tx.Exec("UPDATE schema_version SET version = 3"); err != nil {
		return fmt.Errorf("updating schema version to 3: %w", err)
	}

	return tx.Commit()
}

// migrateV4 creates the tool_outputs FTS5 virtual table for indexing and
// searching tool execution outputs. The table uses the porter tokenizer for
// improved full-text search. Advances schema_version to 4.
func (s *SQLiteStore) migrateV4() error {
	 tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Create the tool_outputs FTS5 virtual table with porter tokenizer
	if _, err := tx.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS tool_outputs USING fts5(
			id,
			tool_name,
			command,
			content,
			truncated,
			exit_code,
			timestamp,
			tokenize="porter unicode61"
		)
	`); err != nil {
		return fmt.Errorf("creating tool_outputs FTS5 table: %w", err)
	}

	if _, err := tx.Exec("UPDATE schema_version SET version = 4"); err != nil {
		return fmt.Errorf("updating schema version to 4: %w", err)
	}

	return tx.Commit()
}
