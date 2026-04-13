package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin:    func(r *http.Request) bool { return true },
	ReadBufferSize: 1024,
	WriteBufferSize: 1024,
}

// handleMetricsWebSocket upgrades the connection to WebSocket and pushes a
// MetricsSnapshot every 5 seconds until the client disconnects.
func (s *Server) handleMetricsWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("web: ws/metrics upgrade error", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(4096) // control frames only
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Send an initial snapshot immediately.
	snap := s.buildMetricsSnapshot(r.Context())
	if err := conn.WriteJSON(snap); err != nil {
		return
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Pump control messages to detect client close.
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
		case <-r.Context().Done():
			return
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		case <-ticker.C:
			snap := s.buildMetricsSnapshot(r.Context())
			if err := conn.WriteJSON(snap); err != nil {
				return
			}
		}
	}
}
