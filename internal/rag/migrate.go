package rag

import (
	"database/sql"
	"fmt"
)

// MigrateV9 creates the documents, document_chunks, and document_chunks_fts
// tables (plus sync triggers) needed by the RAG subsystem.
//
// It is idempotent — all CREATE statements use IF NOT EXISTS — and is safe to
// call on an existing database that already contains these tables.
//
// Note: FOREIGN KEY enforcement must be enabled by the caller (either via the
// DSN pragma or an explicit PRAGMA foreign_keys = ON before calling this).
func MigrateV9(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmts := []string{
		// ── Core tables ───────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS documents (
			id            TEXT PRIMARY KEY,
			namespace     TEXT NOT NULL DEFAULT 'global',
			title         TEXT NOT NULL,
			source_sha256 TEXT,
			mime          TEXT,
			chunk_count   INTEGER DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS document_chunks (
			id          TEXT PRIMARY KEY,
			doc_id      TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			idx         INTEGER NOT NULL,
			content     TEXT NOT NULL,
			embedding   BLOB,
			token_count INTEGER DEFAULT 0,
			UNIQUE(doc_id, idx)
		)`,

		// ── FTS5 virtual table ────────────────────────────────────────────────
		// content= points at the base table so FTS5 can use a content table
		// strategy; content_rowid= links FTS rowids to base table rowids.
		`CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
			content,
			content=document_chunks,
			content_rowid=rowid
		)`,

		// ── Sync triggers ─────────────────────────────────────────────────────
		// Same pattern as memory_fts triggers in the main migration.go.
		`CREATE TRIGGER IF NOT EXISTS dc_ai
			AFTER INSERT ON document_chunks BEGIN
				INSERT INTO document_chunks_fts(rowid, content)
				VALUES (new.rowid, new.content);
			END`,

		`CREATE TRIGGER IF NOT EXISTS dc_ad
			AFTER DELETE ON document_chunks BEGIN
				INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content)
				VALUES ('delete', old.rowid, old.content);
			END`,

		`CREATE TRIGGER IF NOT EXISTS dc_au
			AFTER UPDATE ON document_chunks BEGIN
				INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content)
				VALUES ('delete', old.rowid, old.content);
				INSERT INTO document_chunks_fts(rowid, content)
				VALUES (new.rowid, new.content);
			END`,
	}

	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("exec statement: %w\nSQL: %s", err, s)
		}
	}

	return tx.Commit()
}
