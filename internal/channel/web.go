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

// WebChannel is a Channel + StreamSender backed by WebSocket connections.
// Each connected client gets a unique channelID "web:<uuid-prefix>" and its
// own *websocket.Conn stored in the sync.Map.
type WebChannel struct {
	conns    sync.Map // map[string]*websocket.Conn — channelID → conn
	inbox    chan<- IncomingMessage
	upgrader websocket.Upgrader
}

// NewWebChannel creates a WebChannel ready to accept connections.
// Call Start() before HandleWebSocket connections arrive so the inbox is set.
func NewWebChannel() *WebChannel {
	return &WebChannel{
		upgrader: websocket.Upgrader{
			// Same-origin is fine: the frontend is served from the same server.
			CheckOrigin: func(r *http.Request) bool { return true },
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
		if conn, ok := value.(*websocket.Conn); ok {
			conn.Close()
		}
		w.conns.Delete(key)
		return true
	})
	return nil
}

// Send delivers a full (non-streaming) message to the connection identified by
// msg.ChannelID. Returns an error if the connection is not found.
func (w *WebChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	conn, ok := w.conns.Load(msg.ChannelID)
	if !ok {
		return fmt.Errorf("web: connection %s not found", msg.ChannelID)
	}
	wsConn := conn.(*websocket.Conn)
	payload, err := json.Marshal(wsMsg{
		Type:      "message",
		Text:      msg.Text,
		ChannelID: msg.ChannelID,
	})
	if err != nil {
		return fmt.Errorf("web: marshal send: %w", err)
	}
	return wsConn.WriteMessage(websocket.TextMessage, payload)
}

// HandleWebSocket upgrades an HTTP request to a WebSocket connection, assigns
// it a unique channelID, and blocks reading messages until the client disconnects.
// Register this as: mux.HandleFunc("/ws/chat", webChannel.HandleWebSocket)
func (w *WebChannel) HandleWebSocket(rw http.ResponseWriter, r *http.Request) {
	conn, err := w.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	connID := "web:" + uuid.New().String()[:8]
	w.conns.Store(connID, conn)
	slog.Info("websocket client connected", "channel_id", connID)

	defer func() {
		w.conns.Delete(connID)
		conn.Close()
		slog.Info("websocket client disconnected", "channel_id", connID)
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
	conn, ok := w.conns.Load(channelID)
	if !ok {
		return nil, fmt.Errorf("web: connection %s not found", channelID)
	}
	return &webStreamWriter{conn: conn.(*websocket.Conn), channelID: channelID}, nil
}

// Compile-time assertions.
var (
	_ Channel      = (*WebChannel)(nil)
	_ StreamSender = (*WebChannel)(nil)
)

// --------------------------------------------------------------------------
// webStreamWriter
// --------------------------------------------------------------------------

// webStreamWriter writes incremental token chunks to a single WebSocket client.
// A mutex is required because gorilla/websocket does not allow concurrent writes.
type webStreamWriter struct {
	conn      *websocket.Conn
	channelID string
	mu        sync.Mutex
}

// WriteChunk sends a stream_chunk frame.
func (sw *webStreamWriter) WriteChunk(text string) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	payload, err := json.Marshal(wsMsg{
		Type:      "token",
		Text:      text,
		ChannelID: sw.channelID,
	})
	if err != nil {
		return fmt.Errorf("web: marshal chunk: %w", err)
	}
	return sw.conn.WriteMessage(websocket.TextMessage, payload)
}

// Finalize sends a stream_end frame signalling that the stream is complete.
func (sw *webStreamWriter) Finalize() error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	payload, err := json.Marshal(wsMsg{
		Type:      "done",
		ChannelID: sw.channelID,
	})
	if err != nil {
		return fmt.Errorf("web: marshal finalize: %w", err)
	}
	return sw.conn.WriteMessage(websocket.TextMessage, payload)
}

// Abort sends an error frame.
func (sw *webStreamWriter) Abort(e error) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	payload, err := json.Marshal(wsMsg{
		Type:      "error",
		Message:   e.Error(),
		ChannelID: sw.channelID,
	})
	if err != nil {
		return fmt.Errorf("web: marshal abort: %w", err)
	}
	return sw.conn.WriteMessage(websocket.TextMessage, payload)
}
