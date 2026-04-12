package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const auditSchema = `
CREATE TABLE IF NOT EXISTS audit_events (
    id            TEXT PRIMARY KEY,
    scope_id      TEXT NOT NULL,
    event_type    TEXT NOT NULL,
    timestamp     DATETIME NOT NULL,
    duration_ms   INTEGER NOT NULL,
    model         TEXT,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    stop_reason   TEXT,
    iteration     INTEGER,
    tool_name     TEXT,
    tool_ok       INTEGER,
    details       TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_scope ON audit_events(scope_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_type  ON audit_events(event_type, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_ts    ON audit_events(timestamp DESC);
`

// SQLiteAuditor writes audit events to a SQLite database at {basePath}/audit.db.
// Safe for concurrent use. Open via NewSQLiteAuditor; close via Close.
type SQLiteAuditor struct {
	db        *sql.DB
	closeOnce sync.Once
}

// NewSQLiteAuditor opens (or creates) audit.db at basePath, enables WAL mode,
// and applies the schema. Returns a non-nil error if any step fails.
func NewSQLiteAuditor(basePath string) (*SQLiteAuditor, error) {
	if err := os.MkdirAll(basePath, 0o750); err != nil {
		return nil, fmt.Errorf("audit: create directory %q: %w", basePath, err)
	}
	dbPath := filepath.Join(basePath, "audit.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("audit: open %q: %w", dbPath, err)
	}
	// SQLite allows only one writer at a time; serialize writes through a single
	// connection to avoid SQLITE_BUSY errors under concurrent Emit calls.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: enable WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: set busy timeout: %w", err)
	}
	if _, err := db.Exec(auditSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: init schema: %w", err)
	}
	return &SQLiteAuditor{db: db}, nil
}

// Emit persists event as a single row. Duplicate IDs are silently ignored.
func (a *SQLiteAuditor) Emit(ctx context.Context, event AuditEvent) error {
	var toolOK int
	if event.ToolOK {
		toolOK = 1
	}
	var detailsJSON string
	if len(event.Details) > 0 {
		b, err := json.Marshal(event.Details)
		if err != nil {
			return fmt.Errorf("audit: marshal details: %w", err)
		}
		detailsJSON = string(b)
	}
	_, err := a.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO audit_events
		 (id, scope_id, event_type, timestamp, duration_ms,
		  model, input_tokens, output_tokens, stop_reason, iteration,
		  tool_name, tool_ok, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.ScopeID, event.EventType, event.Timestamp, event.DurationMs,
		event.Model, event.InputTokens, event.OutputTokens, event.StopReason, event.Iteration,
		event.ToolName, toolOK, detailsJSON,
	)
	if err != nil {
		return fmt.Errorf("audit: emit event %q: %w", event.ID, err)
	}
	return nil
}

// LogStreamer is an optional interface for streaming recent audit events as log
// lines. Only SQLiteAuditor implements this interface.
type LogStreamer interface {
	// RecentEvents returns up to limit audit events newer than afterID, ordered
	// by timestamp ascending. Pass afterID="" to get the most recent limit rows.
	RecentEvents(ctx context.Context, afterID string, limit int) ([]AuditEvent, error)
}

// RecentEvents returns up to limit audit events. When afterID is empty the
// most recent rows are returned ordered newest-first so callers can prime the
// cursor; when afterID is set, rows with a timestamp strictly after that event
// are returned ordered oldest-first (polling mode).
func (a *SQLiteAuditor) RecentEvents(ctx context.Context, afterID string, limit int) ([]AuditEvent, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if afterID == "" {
		// Initial load: last `limit` rows ordered newest-first.
		rows, err = a.db.QueryContext(ctx, `
			SELECT id, scope_id, event_type, timestamp, duration_ms,
			       COALESCE(model,''), COALESCE(input_tokens,0), COALESCE(output_tokens,0),
			       COALESCE(stop_reason,''), COALESCE(iteration,0),
			       COALESCE(tool_name,''), tool_ok
			FROM audit_events
			ORDER BY timestamp DESC
			LIMIT ?
		`, limit)
	} else {
		// Poll: rows added after the cursor event, oldest-first.
		rows, err = a.db.QueryContext(ctx, `
			SELECT id, scope_id, event_type, timestamp, duration_ms,
			       COALESCE(model,''), COALESCE(input_tokens,0), COALESCE(output_tokens,0),
			       COALESCE(stop_reason,''), COALESCE(iteration,0),
			       COALESCE(tool_name,''), tool_ok
			FROM audit_events
			WHERE timestamp > (SELECT timestamp FROM audit_events WHERE id = ? LIMIT 1)
			ORDER BY timestamp ASC
			LIMIT ?
		`, afterID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("audit: RecentEvents query: %w", err)
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var (
			e      AuditEvent
			toolOK int
		)
		if err := rows.Scan(
			&e.ID, &e.ScopeID, &e.EventType, &e.Timestamp, &e.DurationMs,
			&e.Model, &e.InputTokens, &e.OutputTokens,
			&e.StopReason, &e.Iteration,
			&e.ToolName, &toolOK,
		); err != nil {
			return nil, fmt.Errorf("audit: RecentEvents scan: %w", err)
		}
		e.ToolOK = toolOK == 1
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: RecentEvents rows: %w", err)
	}
	return events, nil
}

// Compile-time assertion that SQLiteAuditor implements LogStreamer.
var _ LogStreamer = (*SQLiteAuditor)(nil)

// Close releases the database connection. Safe to call multiple times.
func (a *SQLiteAuditor) Close() error {
	var closeErr error
	a.closeOnce.Do(func() {
		closeErr = a.db.Close()
	})
	return closeErr
}

// Compile-time interface assertion.
var _ Auditor = (*SQLiteAuditor)(nil)
