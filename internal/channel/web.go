package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"microagent/internal/content"
)

// wsMsg is the wire format for all WebSocket messages (both directions).
type wsMsg struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	SenderID  string `json:"sender_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

// wsConn bundles a WebSocket connection with its write mutex.
// gorilla/websocket requires serial writes; all callers (Send, BeginStream,
// EmitTelemetry) share this single mutex so concurrent writes are safe.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// writeJSON marshals v and sends it as a TextMessage under the write lock.
func (c *wsConn) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, payload)
}

// close closes the underlying connection.
func (c *wsConn) close() { c.conn.Close() }

// WebChannel is a Channel + StreamSender + TelemetryEmitter backed by
// WebSocket connections. Each connected client gets a unique channelID
// "web:<uuid-prefix>" and its own *wsConn stored in the sync.Map.
type WebChannel struct {
	conns    sync.Map // map[string]*wsConn — channelID → wsConn
	inbox    chan<- IncomingMessage
	upgrader websocket.Upgrader
}

const (
	wsMaxMessageSize = 64 * 1024  // 64 KB max inbound message
	wsMaxConnections = 50         // max concurrent WebSocket clients
	wsPongWait       = 60 * time.Second
	wsPingInterval   = 50 * time.Second // must be less than pongWait
)

// NewWebChannel creates a WebChannel ready to accept connections.
// Call Start() before HandleWebSocket connections arrive so the inbox is set.
func NewWebChannel() *WebChannel {
	return &WebChannel{
		upgrader: websocket.Upgrader{
			CheckOrigin:  func(r *http.Request) bool { return true },
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

// Name returns "web".
func (w *WebChannel) Name() string { return "web" }

// Start stores the inbox reference. The channel has no background goroutines of
// its own — connections drive their own read goroutines via HandleWebSocket.
// MUST be called before the first connection arrives.
func (w *WebChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	w.inbox = inbox
	return nil
}

// Stop closes every active WebSocket connection and drains the connection map.
func (w *WebChannel) Stop() error {
	w.conns.Range(func(key, value any) bool {
		if c, ok := value.(*wsConn); ok {
			c.close()
		}
		w.conns.Delete(key)
		return true
	})
	return nil
}

// Send delivers a full (non-streaming) message to the connection identified by
// msg.ChannelID. Returns an error if the connection is not found.
func (w *WebChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	val, ok := w.conns.Load(msg.ChannelID)
	if !ok {
		return fmt.Errorf("web: connection %s not found", msg.ChannelID)
	}
	c := val.(*wsConn)
	return c.writeJSON(wsMsg{
		Type:      "message",
		Text:      msg.Text,
		ChannelID: msg.ChannelID,
	})
}

// EmitTelemetry implements TelemetryEmitter. It sends a telemetry frame to the
// identified connection. Returns nil silently if the connection is gone.
func (w *WebChannel) EmitTelemetry(ctx context.Context, channelID string, frame map[string]any) error {
	val, ok := w.conns.Load(channelID)
	if !ok {
		return nil // silently skip if connection gone
	}
	c := val.(*wsConn)
	frame["channel_id"] = channelID
	return c.writeJSON(frame)
}

// HandleWebSocket upgrades an HTTP request to a WebSocket connection, assigns
// it a unique channelID, and blocks reading messages until the client disconnects.
// Register this as: mux.HandleFunc("/ws/chat", webChannel.HandleWebSocket)
// connCount tracks active connections for the cap.
func (w *WebChannel) connCount() int {
	count := 0
	w.conns.Range(func(_, _ any) bool { count++; return true })
	return count
}

func (w *WebChannel) HandleWebSocket(rw http.ResponseWriter, r *http.Request) {
	// Connection cap.
	if w.connCount() >= wsMaxConnections {
		http.Error(rw, "too many connections", http.StatusServiceUnavailable)
		return
	}

	conn, err := w.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	// Read limits and deadlines.
	conn.SetReadLimit(wsMaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})

	connID := "web:" + uuid.New().String()[:8]
	wc := &wsConn{conn: conn}
	w.conns.Store(connID, wc)
	slog.Info("websocket client connected", "channel_id", connID)

	defer func() {
		w.conns.Delete(connID)
		conn.Close()
		slog.Info("websocket client disconnected", "channel_id", connID)
	}()

	// Ping ticker to detect dead connections.
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()
	go func() {
		for range pingTicker.C {
			wc.mu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			wc.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("websocket read error", "channel_id", connID, "error", err)
			}
			return
		}

		var incoming wsMsg
		if err := json.Unmarshal(msgBytes, &incoming); err != nil {
			slog.Warn("websocket: malformed message", "channel_id", connID, "error", err)
			continue
		}

		if incoming.Type != "message" || incoming.Text == "" {
			continue
		}

		if w.inbox == nil {
			slog.Warn("websocket: inbox not initialised, dropping message", "channel_id", connID)
			continue
		}

		msg := IncomingMessage{
			ID:        uuid.New().String()[:8],
			ChannelID: connID,
			SenderID:  incoming.SenderID,
			Content:   content.TextBlock(incoming.Text),
			Timestamp: time.Now(),
		}

		select {
		case w.inbox <- msg:
		default:
			slog.Warn("websocket: inbox full, dropping message", "channel_id", connID)
		}
	}
}

// BeginStream implements StreamSender. It returns a StreamWriter that sends
// stream_chunk / stream_end / error frames to the identified connection.
func (w *WebChannel) BeginStream(ctx context.Context, channelID string) (StreamWriter, error) {
	val, ok := w.conns.Load(channelID)
	if !ok {
		return nil, fmt.Errorf("web: connection %s not found", channelID)
	}
	return &webStreamWriter{wc: val.(*wsConn), channelID: channelID}, nil
}

// Compile-time assertions.
var (
	_ Channel          = (*WebChannel)(nil)
	_ StreamSender     = (*WebChannel)(nil)
	_ TelemetryEmitter = (*WebChannel)(nil)
)

// --------------------------------------------------------------------------
// webStreamWriter
// --------------------------------------------------------------------------

// webStreamWriter writes incremental token chunks to a single WebSocket client.
// It reuses the per-connection mutex from wsConn so concurrent writes with
// Send() and EmitTelemetry() are safe.
type webStreamWriter struct {
	wc        *wsConn
	channelID string
}

// WriteChunk sends a stream_chunk frame.
func (sw *webStreamWriter) WriteChunk(text string) error {
	return sw.wc.writeJSON(wsMsg{
		Type:      "token",
		Text:      text,
		ChannelID: sw.channelID,
	})
}

// Finalize sends a stream_end frame signalling that the stream is complete.
func (sw *webStreamWriter) Finalize() error {
	return sw.wc.writeJSON(wsMsg{
		Type:      "done",
		ChannelID: sw.channelID,
	})
}

// Abort sends an error frame.
func (sw *webStreamWriter) Abort(e error) error {
	return sw.wc.writeJSON(wsMsg{
		Type:      "error",
		Message:   e.Error(),
		ChannelID: sw.channelID,
	})
}
