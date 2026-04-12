package store

import (
	"strings"
	"testing"
)

// TestMigration_V4_ToolOutputsFTSTable verifies that migration v4 creates the
// tool_outputs FTS5 virtual table with the correct schema.
func TestMigration_V4_ToolOutputsFTSTable(t *testing.T) {
	s := newTestSQLiteStore(t)

	// Verify tool_outputs FTS5 table exists
	var count int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='tool_outputs'",
	).Scan(&count); err != nil {
		t.Fatalf("querying tool_outputs table: %v", err)
	}
	if count != 1 {
		t.Errorf("expected tool_outputs table to exist, count=%d", count)
	}

	// Verify schema includes expected columns
	var schema string
	if err := s.db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='tool_outputs'",
	).Scan(&schema); err != nil {
		t.Fatalf("querying tool_outputs schema: %v", err)
	}

	expectedCols := []string{"id", "tool_name", "command", "content", "truncated", "exit_code", "timestamp"}
	for _, col := range expectedCols {
		if !strings.Contains(schema, col) {
			t.Errorf("expected column %q in tool_outputs schema: %s", col, schema)
		}
	}

	// Verify FTS5 uses porter tokenizer
	if !strings.Contains(schema, "porter") {
		t.Errorf("expected porter tokenizer in tool_outputs schema: %s", schema)
	}
}

// TestMigration_V4_SchemaVersionIs4 verifies that migration v4 ran; the final
// version is now 5 because v5 (media_blobs) runs after v4 on a fresh DB.
func TestMigration_V4_SchemaVersionIs4(t *testing.T) {
	s := newTestSQLiteStore(t)

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 5 {
		t.Errorf("expected schema_version=5 (v4+v5 both applied), got %d", version)
	}
}

// TestMigration_V4_RerunIsNoOp verifies that calling initSchema a second time
// on an already-v4 database is safe and leaves schema_version unchanged.
func TestMigration_V4_RerunIsNoOp(t *testing.T) {
	path := t.TempDir()
	s := openSQLiteStoreAt(t, path)

	// Run migrations a second time
	if err := s.initSchema(); err != nil {
		t.Fatalf("second initSchema: %v", err)
	}

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 5 {
		t.Errorf("expected schema_version=5 after re-run, got %d", version)
	}
	s.Close()
}
