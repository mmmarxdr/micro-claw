package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestNewSQLiteAuditor_CreatesDirectoryAndDB verifies that NewSQLiteAuditor creates
// the basePath directory and the audit.db file inside it.
func TestNewSQLiteAuditor_CreatesDirectoryAndDB(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "newdir")
	a, err := NewSQLiteAuditor(basePath)
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil auditor")
	}
	dbPath := filepath.Join(basePath, "audit.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected audit.db at %q: %v", dbPath, err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestNewSQLiteAuditor_SchemaIsIdempotent verifies that calling NewSQLiteAuditor
// twice on the same path does not produce an error (CREATE TABLE IF NOT EXISTS).
func TestNewSQLiteAuditor_SchemaIsIdempotent(t *testing.T) {
	basePath := t.TempDir()

	a1, err := NewSQLiteAuditor(basePath)
	if err != nil {
		t.Fatalf("first NewSQLiteAuditor: %v", err)
	}
	if err := a1.Close(); err != nil {
		t.Errorf("a1.Close: %v", err)
	}

	a2, err := NewSQLiteAuditor(basePath)
	if err != nil {
		t.Fatalf("second NewSQLiteAuditor: %v", err)
	}
	if err := a2.Close(); err != nil {
		t.Errorf("a2.Close: %v", err)
	}
}

// TestNewSQLiteAuditor_BadPath verifies that NewSQLiteAuditor returns an error when
// the parent path is a regular file (MkdirAll will fail because file blocks dir creation).
func TestNewSQLiteAuditor_BadPath(t *testing.T) {
	// Create a regular file at the parent path so that subdirectory creation fails.
	parentFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(parentFile, []byte("block"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Try to create basePath under the file (impossible — file is not a directory).
	_, err := NewSQLiteAuditor(filepath.Join(parentFile, "subdir"))
	if err == nil {
		t.Error("expected error for bad path, got nil")
	}
}

// TestSQLiteAuditor_EmitLLMCallEvent verifies that an LLM call event is persisted
// with correct column values.
func TestSQLiteAuditor_EmitLLMCallEvent(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	event := AuditEvent{
		ID:           "e1",
		ScopeID:      "cli",
		EventType:    "llm_call",
		Timestamp:    time.Now(),
		InputTokens:  100,
		OutputTokens: 50,
		Model:        "claude-3-5-sonnet",
	}
	if err := a.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var scopeID, eventType, model string
	var inputTokens, outputTokens int
	row := a.db.QueryRow(
		`SELECT scope_id, event_type, input_tokens, output_tokens, model
		 FROM audit_events WHERE id = 'e1'`,
	)
	if err := row.Scan(&scopeID, &eventType, &inputTokens, &outputTokens, &model); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scopeID != "cli" {
		t.Errorf("scope_id: want %q, got %q", "cli", scopeID)
	}
	if eventType != "llm_call" {
		t.Errorf("event_type: want %q, got %q", "llm_call", eventType)
	}
	if inputTokens != 100 {
		t.Errorf("input_tokens: want 100, got %d", inputTokens)
	}
	if outputTokens != 50 {
		t.Errorf("output_tokens: want 50, got %d", outputTokens)
	}
	if model != "claude-3-5-sonnet" {
		t.Errorf("model: want %q, got %q", "claude-3-5-sonnet", model)
	}
}

// TestSQLiteAuditor_EmitToolUseEvent verifies that tool_ok is stored as 1 and
// details is stored as valid JSON.
func TestSQLiteAuditor_EmitToolUseEvent(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	event := AuditEvent{
		ID:        "e2",
		ScopeID:   "cli",
		EventType: "tool_use",
		Timestamp: time.Now(),
		ToolName:  "shell_exec",
		ToolOK:    true,
		Details:   map[string]string{"exit_code": "0"},
	}
	if err := a.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var toolName string
	var toolOK int
	var detailsStr string
	row := a.db.QueryRow(
		`SELECT tool_name, tool_ok, details FROM audit_events WHERE id = 'e2'`,
	)
	if err := row.Scan(&toolName, &toolOK, &detailsStr); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if toolName != "shell_exec" {
		t.Errorf("tool_name: want %q, got %q", "shell_exec", toolName)
	}
	if toolOK != 1 {
		t.Errorf("tool_ok: want 1, got %d", toolOK)
	}
	var details map[string]string
	if err := json.Unmarshal([]byte(detailsStr), &details); err != nil {
		t.Fatalf("json.Unmarshal details: %v", err)
	}
	if details["exit_code"] != "0" {
		t.Errorf("details[exit_code]: want %q, got %q", "0", details["exit_code"])
	}
}

// TestSQLiteAuditor_ScopeNonIsolation verifies that events from multiple scopes
// are stored in the same table and no JSONL files are created.
func TestSQLiteAuditor_ScopeNonIsolation(t *testing.T) {
	basePath := t.TempDir()
	a, err := NewSQLiteAuditor(basePath)
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	events := []AuditEvent{
		{ID: "s1", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now()},
		{ID: "s2", ScopeID: "telegram:12345", EventType: "llm_call", Timestamp: time.Now()},
	}
	for _, e := range events {
		if err := a.Emit(context.Background(), e); err != nil {
			t.Fatalf("Emit %q: %v", e.ID, err)
		}
	}

	for _, scopeID := range []string{"cli", "telegram:12345"} {
		var count int
		row := a.db.QueryRow(
			`SELECT COUNT(*) FROM audit_events WHERE scope_id = ?`, scopeID,
		)
		if err := row.Scan(&count); err != nil {
			t.Fatalf("Scan count for %q: %v", scopeID, err)
		}
		if count != 1 {
			t.Errorf("scope %q: want 1 row, got %d", scopeID, count)
		}
	}

	// Assert no JSONL files were created. WAL mode may produce audit.db-shm and
	// audit.db-wal sidecar files; those are expected and allowed.
	entries, err := os.ReadDir(basePath)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name != "audit.db" && name != "audit.db-shm" && name != "audit.db-wal" {
			t.Errorf("unexpected file in basePath: %q (only audit.db and WAL sidecars expected)", name)
		}
	}
}

// TestSQLiteAuditor_DuplicateIDIsIgnored verifies that emitting the same event ID
// twice silently succeeds and only the first row is retained.
func TestSQLiteAuditor_DuplicateIDIsIgnored(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	first := AuditEvent{ID: "e1", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now(), Model: "first"}
	second := AuditEvent{ID: "e1", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now(), Model: "second"}

	if err := a.Emit(context.Background(), first); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	if err := a.Emit(context.Background(), second); err != nil {
		t.Fatalf("second Emit: %v", err)
	}

	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE id = 'e1'`).Scan(&count); err != nil {
		t.Fatalf("Scan count: %v", err)
	}
	if count != 1 {
		t.Errorf("want 1 row, got %d", count)
	}

	var model string
	if err := a.db.QueryRow(`SELECT model FROM audit_events WHERE id = 'e1'`).Scan(&model); err != nil {
		t.Fatalf("Scan model: %v", err)
	}
	if model != "first" {
		t.Errorf("model: want %q (first-write wins), got %q", "first", model)
	}
}

// TestSQLiteAuditor_ConcurrentEmit verifies that N goroutines emitting concurrently
// all succeed and all rows are present.
func TestSQLiteAuditor_ConcurrentEmit(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	const n = 10
	errs := make(chan error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			event := AuditEvent{
				ID:        fmt.Sprintf("concurrent-%d", i),
				ScopeID:   "cli",
				EventType: "llm_call",
				Timestamp: time.Now(),
			}
			errs <- a.Emit(context.Background(), event)
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("Emit error: %v", err)
		}
	}

	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatalf("Scan count: %v", err)
	}
	if count != n {
		t.Errorf("want %d rows, got %d", n, count)
	}
}

// TestSQLiteAuditor_EmitAfterClose verifies that Emit after Close returns an error
// (exercises the db.ExecContext error path).
func TestSQLiteAuditor_EmitAfterClose(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close, ExecContext should return an error.
	err = a.Emit(context.Background(), AuditEvent{
		ID: "after-close", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now(),
	})
	if err == nil {
		t.Error("expected error from Emit after Close, got nil")
	}
}

// TestSQLiteAuditor_EmitNilDetails verifies that Emit with nil Details does not
// attempt JSON marshalling and stores an empty string.
func TestSQLiteAuditor_EmitNilDetails(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	defer a.Close()

	event := AuditEvent{
		ID:        "nil-details",
		ScopeID:   "cli",
		EventType: "llm_call",
		Timestamp: time.Now(),
		Details:   nil,
	}
	if err := a.Emit(context.Background(), event); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var details *string
	row := a.db.QueryRow(`SELECT details FROM audit_events WHERE id = 'nil-details'`)
	if err := row.Scan(&details); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// details should be empty string (not null, since we always bind a string)
	if details != nil && *details != "" {
		t.Errorf("details: want empty, got %q", *details)
	}
}

// TestSQLiteAuditor_CloseIdempotency verifies that Close can be called multiple times
// without panic or error.
func TestSQLiteAuditor_CloseIdempotency(t *testing.T) {
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}

	if err := a.Emit(context.Background(), AuditEvent{
		ID: "x1", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if err := a.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
