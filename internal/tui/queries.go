// Package tui provides terminal UI components for the microagent dashboard.
package tui

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	// Register the SQLite driver.
	_ "modernc.org/sqlite"

	"microagent/internal/config"
)

// OverviewData holds aggregate statistics from the audit database.
type OverviewData struct {
	AuditDBPath     string
	TotalEvents     int64
	LLMCalls        int64
	AvgTokensIn     float64
	AvgTokensOut    float64
	ToolCalls       int64
	ToolSuccessRate float64
	LastEventAt     string // formatted timestamp or ""
	NoData          bool   // true if DB file not found
}

// AuditEventRow holds a single row from the audit_events table.
type AuditEventRow struct {
	ID         string
	EventType  string
	Model      string
	TokensIn   int64
	TokensOut  int64
	DurationMs int64
	ToolOK     bool
}

// StoreStats holds summary counts from the store database.
type StoreStats struct {
	Conversations int64
	MemoryEntries int64
	Secrets       int64
	NoData        bool // true if DB file not found
}

// LoadAll queries both databases and returns all dashboard data.
// If a database file does not exist, the corresponding struct has NoData=true.
// Other errors (schema mismatch, permission denied) are returned in err.
func LoadAll(cfg *config.Config) (OverviewData, []AuditEventRow, StoreStats, MCPTabData, error) {
	auditDBPath := filepath.Join(cfg.Audit.Path, "audit.db")
	storeDBPath := filepath.Join(cfg.Store.Path, "microagent.db")

	overview, auditEvents, err1 := loadAuditData(auditDBPath)
	storeStats, err2 := loadStoreData(storeDBPath)
	mcpData := loadMCPData(cfg)

	if err1 != nil {
		return overview, auditEvents, storeStats, mcpData, err1
	}
	return overview, auditEvents, storeStats, mcpData, err2
}

// loadAuditData queries the audit database at dbPath.
// Returns NoData=true (no error) if the file does not exist.
func loadAuditData(dbPath string) (OverviewData, []AuditEventRow, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return OverviewData{AuditDBPath: dbPath, NoData: true}, nil, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return OverviewData{}, nil, fmt.Errorf("queries: open audit db: %w", err)
	}
	defer db.Close() //nolint:errcheck

	var overview OverviewData
	overview.AuditDBPath = dbPath

	// Total events.
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&overview.TotalEvents)

	// LLM call stats.
	_ = db.QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(input_tokens),0), COALESCE(AVG(output_tokens),0)
		FROM audit_events WHERE event_type = 'llm_call'
	`).Scan(&overview.LLMCalls, &overview.AvgTokensIn, &overview.AvgTokensOut)

	// Tool call stats.
	_ = db.QueryRow(`
		SELECT COUNT(*), COALESCE(CAST(SUM(CASE WHEN tool_ok=1 THEN 1 ELSE 0 END) AS REAL) / COUNT(*) * 100, 0)
		FROM audit_events WHERE event_type = 'tool_call'
	`).Scan(&overview.ToolCalls, &overview.ToolSuccessRate)

	// Last event timestamp.
	_ = db.QueryRow(`
		SELECT COALESCE(timestamp,'') FROM audit_events ORDER BY timestamp DESC LIMIT 1
	`).Scan(&overview.LastEventAt)

	// Last 50 events.
	rows, err := db.Query(`
		SELECT COALESCE(id,''), event_type, COALESCE(model,''),
		       COALESCE(input_tokens,0), COALESCE(output_tokens,0),
		       COALESCE(duration_ms,0), COALESCE(tool_ok,0)
		FROM audit_events ORDER BY timestamp DESC LIMIT 50
	`)
	if err != nil {
		// Table might not exist yet; return overview with no events.
		return overview, nil, nil //nolint:nilerr
	}
	defer rows.Close() //nolint:errcheck

	var events []AuditEventRow
	for rows.Next() {
		var r AuditEventRow
		var toolOK int
		if err := rows.Scan(&r.ID, &r.EventType, &r.Model,
			&r.TokensIn, &r.TokensOut, &r.DurationMs, &toolOK); err != nil {
			return overview, events, fmt.Errorf("queries: scan event: %w", err)
		}
		r.ToolOK = toolOK == 1
		events = append(events, r)
	}
	if err := rows.Err(); err != nil {
		return overview, events, fmt.Errorf("queries: iterate events: %w", err)
	}
	return overview, events, nil
}

// loadStoreData queries the store database at dbPath.
// Returns NoData=true (no error) if the file does not exist.
func loadStoreData(dbPath string) (StoreStats, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return StoreStats{NoData: true}, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return StoreStats{}, fmt.Errorf("queries: open store db: %w", err)
	}
	defer db.Close() //nolint:errcheck

	var stats StoreStats
	_ = db.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&stats.Conversations)
	_ = db.QueryRow(`SELECT COUNT(*) FROM memory`).Scan(&stats.MemoryEntries)
	_ = db.QueryRow(`SELECT COUNT(*) FROM secrets`).Scan(&stats.Secrets)
	return stats, nil
}
