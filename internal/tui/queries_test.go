package tui

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"daimon/internal/config"
)

func TestLoadAuditData_NoDBFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "audit.db")

	overview, events, err := loadAuditData(path)
	if err != nil {
		t.Fatalf("loadAuditData: expected no error for missing DB, got: %v", err)
	}
	if !overview.NoData {
		t.Error("expected overview.NoData == true for missing DB")
	}
	if len(events) != 0 {
		t.Errorf("expected no events for missing DB, got %d", len(events))
	}
}

func TestLoadAuditData_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")

	createAuditDB(t, dbPath)

	overview, events, err := loadAuditData(dbPath)
	if err != nil {
		t.Fatalf("loadAuditData: %v", err)
	}
	if overview.NoData {
		t.Error("expected NoData == false for existing (empty) DB")
	}
	if overview.TotalEvents != 0 {
		t.Errorf("expected TotalEvents == 0, got %d", overview.TotalEvents)
	}
	if len(events) != 0 {
		t.Errorf("expected no events from empty DB, got %d", len(events))
	}
}

func TestLoadAuditData_WithRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "audit.db")

	db := createAuditDB(t, dbPath)
	insertAuditEvent(t, db, "llm_call", "claude-3-5-sonnet-20241022", 100, 50, 200, 0)
	insertAuditEvent(t, db, "llm_call", "claude-3-5-sonnet-20241022", 200, 80, 300, 0)
	insertAuditEvent(t, db, "tool_call", "", 0, 0, 50, 1)
	db.Close()

	overview, events, err := loadAuditData(dbPath)
	if err != nil {
		t.Fatalf("loadAuditData: %v", err)
	}
	if overview.TotalEvents != 3 {
		t.Errorf("TotalEvents = %d, want 3", overview.TotalEvents)
	}
	if overview.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2", overview.LLMCalls)
	}
	if overview.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", overview.ToolCalls)
	}
	if len(events) != 3 {
		t.Errorf("len(events) = %d, want 3", len(events))
	}
}

func TestLoadStoreData_NoDBFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "microagent.db")

	stats, err := loadStoreData(path)
	if err != nil {
		t.Fatalf("loadStoreData: expected no error for missing DB, got: %v", err)
	}
	if !stats.NoData {
		t.Error("expected NoData == true for missing DB")
	}
}

func TestLoadStoreData_WithRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "microagent.db")

	db := createStoreDB(t, dbPath)
	// Insert 1 conversation, 2 memory entries, 3 secrets.
	mustExec(t, db, `INSERT INTO conversations (id, data) VALUES ('c1', '{}')`)
	mustExec(t, db, `INSERT INTO memory (id, data) VALUES ('m1', '{}')`)
	mustExec(t, db, `INSERT INTO memory (id, data) VALUES ('m2', '{}')`)
	mustExec(t, db, `INSERT INTO secrets (id, data) VALUES ('s1', '{}')`)
	mustExec(t, db, `INSERT INTO secrets (id, data) VALUES ('s2', '{}')`)
	mustExec(t, db, `INSERT INTO secrets (id, data) VALUES ('s3', '{}')`)
	db.Close()

	stats, err := loadStoreData(dbPath)
	if err != nil {
		t.Fatalf("loadStoreData: %v", err)
	}
	if stats.NoData {
		t.Error("expected NoData == false for existing DB")
	}
	if stats.Conversations != 1 {
		t.Errorf("Conversations = %d, want 1", stats.Conversations)
	}
	if stats.MemoryEntries != 2 {
		t.Errorf("MemoryEntries = %d, want 2", stats.MemoryEntries)
	}
	if stats.Secrets != 3 {
		t.Errorf("Secrets = %d, want 3", stats.Secrets)
	}
}

func TestLoadAll_BothDBsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Audit: config.AuditConfig{
			Path: filepath.Join(dir, "nonexistent-audit"),
		},
		Store: config.StoreConfig{
			Path: filepath.Join(dir, "nonexistent-store"),
		},
	}

	overview, _, storeStats, _, err := LoadAll(cfg)
	if err != nil {
		t.Fatalf("LoadAll: expected no error for missing DBs, got: %v", err)
	}
	if !overview.NoData {
		t.Error("expected overview.NoData == true")
	}
	if !storeStats.NoData {
		t.Error("expected storeStats.NoData == true")
	}
}

// ------- helpers -------

func createAuditDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open audit DB: %v", err)
	}
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS audit_events (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		timestamp TEXT,
		model TEXT,
		input_tokens INTEGER,
		output_tokens INTEGER,
		duration_ms INTEGER,
		tool_ok INTEGER DEFAULT 0
	)`)
	return db
}

func insertAuditEvent(t *testing.T, db *sql.DB, eventType, model string, tokIn, tokOut, durMs int, toolOK int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO audit_events
		(id, event_type, timestamp, model, input_tokens, output_tokens, duration_ms, tool_ok)
		VALUES (lower(hex(randomblob(4))), ?, datetime('now'), ?, ?, ?, ?, ?)`,
		eventType, model, tokIn, tokOut, durMs, toolOK)
}

func createStoreDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open store DB: %v", err)
	}
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS conversations (id TEXT PRIMARY KEY, data TEXT)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS memory (id TEXT PRIMARY KEY, data TEXT)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS secrets (id TEXT PRIMARY KEY, data TEXT)`)
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
