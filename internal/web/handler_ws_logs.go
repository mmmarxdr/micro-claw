package web

import (
	"log"
	"net/http"
	"strings"
	"time"

	"microagent/internal/audit"
)

// wsLogEntry is the JSON frame shape expected by the frontend LogsPage.
// Fields: time (RFC3339), level (DEBUG/INFO/WARN/ERROR), msg, plus any extras.
type wsLogEntry struct {
	Time       string `json:"time"`
	Level      string `json:"level"`
	Msg        string `json:"msg"`
	EventType  string `json:"event_type,omitempty"`
	ScopeID    string `json:"scope_id,omitempty"`
	Model      string `json:"model,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// auditEventToLogEntry converts an AuditEvent to a wsLogEntry.
func auditEventToLogEntry(e audit.AuditEvent) wsLogEntry {
	level := "INFO"
	var msg string

	switch e.EventType {
	case "llm_call":
		msg = "LLM call"
		if e.Model != "" {
			msg += " model=" + e.Model
		}
		if e.StopReason != "" && e.StopReason != "end_turn" {
			msg += " stop=" + e.StopReason
		}
	case "tool_use":
		if e.ToolOK {
			msg = "Tool OK: " + e.ToolName
		} else {
			level = "WARN"
			msg = "Tool FAIL: " + e.ToolName
		}
	default:
		msg = strings.ReplaceAll(e.EventType, "_", " ")
	}

	entry := wsLogEntry{
		Time:       e.Timestamp.UTC().Format(time.RFC3339Nano),
		Level:      level,
		Msg:        msg,
		EventType:  e.EventType,
		DurationMs: e.DurationMs,
	}
	if e.ScopeID != "" {
		entry.ScopeID = e.ScopeID
	}
	if e.Model != "" {
		entry.Model = e.Model
	}
	if e.ToolName != "" {
		entry.ToolName = e.ToolName
	}
	return entry
}

// handleLogsWebSocket streams audit events as structured JSON log frames.
// On connect, the last 100 events are sent; then new events are polled every 2s.
func (s *Server) handleLogsWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("web: ws/logs upgrade error: %v", err)
		return
	}
	defer conn.Close()

	streamer, ok := s.deps.Auditor.(audit.LogStreamer)
	if !ok {
		// Audit backend does not support streaming — send a synthetic notice and hold.
		_ = conn.WriteJSON(wsLogEntry{
			Time:  time.Now().UTC().Format(time.RFC3339),
			Level: "INFO",
			Msg:   "Log streaming not available (audit backend does not support RecentEvents)",
		})
		// Block until client disconnects.
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}

	ctx := r.Context()

	// Send recent history (newest-first from DB; reverse to oldest-first for display).
	recent, err := streamer.RecentEvents(ctx, "", 100)
	if err != nil {
		log.Printf("web: ws/logs initial query error: %v", err)
	} else {
		for i := len(recent) - 1; i >= 0; i-- {
			if err := conn.WriteJSON(auditEventToLogEntry(recent[i])); err != nil {
				return
			}
		}
	}

	// Track cursor: ID of the last event we sent.
	var cursorID string
	if len(recent) > 0 {
		cursorID = recent[0].ID // recent[0] is the newest because query is DESC
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Pump incoming frames to detect client disconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, err := streamer.RecentEvents(ctx, cursorID, 50)
			if err != nil {
				log.Printf("web: ws/logs poll error: %v", err)
				continue
			}
			for _, e := range events {
				if err := conn.WriteJSON(auditEventToLogEntry(e)); err != nil {
					return
				}
				cursorID = e.ID
			}
		}
	}
}
