package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"daimon/internal/audit"
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
	conn, err := s.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("web: ws/logs upgrade error", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	// resolveStreamer fetches the current auditor under the server's RLock and
	// type-asserts it to LogStreamer. Returns nil when the active backend does
	// not support streaming (noop or file auditor). Re-calling on each tick
	// ensures that after a hot-swap the connection transparently picks up the
	// new backend — and stops touching the old (closed) one.
	resolveStreamer := func() audit.LogStreamer {
		aud := s.CurrentAuditor()
		ls, _ := aud.(audit.LogStreamer)
		return ls
	}

	// Check initial audit backend — report once if streaming is unavailable.
	initialAud := s.CurrentAuditor()
	if _, ok := initialAud.(audit.LogStreamer); !ok {
		var msg string
		switch initialAud.(type) {
		case audit.NoopAuditor:
			msg = "Audit log is disabled. Set audit.enabled: true in your config (defaults to sqlite, which supports streaming)."
		default:
			msg = "Streaming requires the sqlite audit backend. Set audit.type: sqlite in your config to enable live logs."
		}
		_ = conn.WriteJSON(wsLogEntry{
			Time:      time.Now().UTC().Format(time.RFC3339),
			Level:     "WARN",
			Msg:       msg,
			EventType: "stream_unavailable",
		})
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}

	ctx := r.Context()

	// Send the last 100 events using the current streamer.
	var cursorID string
	if streamer := resolveStreamer(); streamer != nil {
		recent, err := streamer.RecentEvents(ctx, "", 100)
		if err != nil {
			slog.Warn("web: ws/logs initial query error", "error", err)
		} else {
			for i := len(recent) - 1; i >= 0; i-- {
				if err := conn.WriteJSON(auditEventToLogEntry(recent[i])); err != nil {
					return
				}
			}
			if len(recent) > 0 {
				cursorID = recent[0].ID // recent[0] is the newest because query is DESC
			}
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

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
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		case <-ticker.C:
			// Re-fetch the streamer on every tick. If a hot-swap has occurred,
			// this picks up the new backend and drops the reference to the old
			// one — making old.Close() safe (no concurrent readers remain).
			streamer := resolveStreamer()
			if streamer == nil {
				continue
			}
			events, err := streamer.RecentEvents(ctx, cursorID, 50)
			if err != nil {
				slog.Warn("web: ws/logs poll error", "error", err)
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
