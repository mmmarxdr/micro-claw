package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"daimon/internal/content"
	"daimon/internal/store"
)

// wsAttachment describes a media attachment referenced in a wsMsg.
type wsAttachment struct {
	SHA256   string `json:"sha256"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Filename string `json:"filename,omitempty"`
}

// wsMsg is the wire format for all WebSocket messages (both directions).
type wsMsg struct {
	Type        string         `json:"type"`
	Text        string         `json:"text,omitempty"`
	ChannelID   string         `json:"channel_id,omitempty"`
	SenderID    string         `json:"sender_id,omitempty"`
	Message     string         `json:"message,omitempty"`
	Attachments []wsAttachment `json:"attachments,omitempty"`
	// Unlimited carries the "continue without a cap" choice on continue_turn
	// requests. Ignored for other types.
	Unlimited bool `json:"unlimited,omitempty"`
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

// DocExtractor extracts plain text from binary document attachments (PDF,
// DOCX, etc.) so the agent can read them on providers that do not support
// native document blocks (currently: all of them, per anthropic_stream.go,
// openai.go, gemini_stream.go translateBlocks fallback paths).
//
// Implementations are stateless. The wiring layer (cmd/daimon) injects an
// adapter over rag.SelectExtractor so the channel package stays decoupled
// from rag.
type DocExtractor interface {
	Supports(mime string) bool
	Extract(ctx context.Context, data []byte, mime string) (text string, err error)
}

// WebChannel is a Channel + StreamSender + TelemetryEmitter backed by
// WebSocket connections. Each connected client gets a unique channelID
// "web:<uuid-prefix>" and its own *wsConn stored in the sync.Map.
type WebChannel struct {
	conns        sync.Map // map[string]*wsConn — channelID → wsConn
	inbox        chan<- IncomingMessage
	upgrader     websocket.Upgrader
	mediaStore   store.MediaStore // optional; nil when media uploads are not configured
	docExtractor DocExtractor     // optional; nil disables inline document extraction
}

// SetMediaStore wires a MediaStore into the WebChannel so that attachment
// SHA-256 references can be validated in HandleWebSocket. Call before the
// first connection arrives (e.g. after NewServer wires its dependencies).
func (w *WebChannel) SetMediaStore(ms store.MediaStore) {
	w.mediaStore = ms
}

// SetDocExtractor wires a DocExtractor so that document attachments
// (PDF/DOCX/text) are inlined as BlockText (with ExtractedFromAttachment=true)
// instead of the placeholder "[document attached: ..., not processed by current
// model]" path that providers fall through to. Call before the first
// connection arrives. Pass nil to disable inline extraction.
func (w *WebChannel) SetDocExtractor(d DocExtractor) {
	w.docExtractor = d
}

const (
	wsMaxMessageSize = 64 * 1024  // 64 KB max inbound message
	wsMaxConnections = 50         // max concurrent WebSocket clients
	wsPongWait       = 60 * time.Second
	wsPingInterval   = 50 * time.Second // must be less than pongWait
)

// NewWebChannel creates a WebChannel ready to accept connections.
// allowedOrigins controls which WebSocket origins are permitted. If nil or
// empty (or contains "*"), all origins are allowed (backwards-compatible default).
// Call Start() before HandleWebSocket connections arrive so the inbox is set.
func NewWebChannel(allowedOrigins ...string) *WebChannel {
	allowAll := len(allowedOrigins) == 0
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
		}
		originSet[strings.TrimRight(o, "/")] = true
	}

	return &WebChannel{
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				if allowAll {
					return true
				}
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true // same-origin requests have no Origin header
				}
				return originSet[strings.TrimRight(origin, "/")]
			},
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

	// Identity handoff for Resume: if the client sent
	// `?conversation_id=<id>`, every IncomingMessage emitted from this
	// connection carries that convID. Downstream, agent.processMessage
	// uses it verbatim and bypasses the userScope derivation — letting the
	// client pick up a prior conversation across sessions. Absent or
	// empty → pre-existing behavior (userScope fallback).
	//
	// The cap is defensive against pathological query strings. 200 chars
	// comfortably fits any realistic convID ("conv_" + channel + ":" + sender).
	resumedConvID := strings.TrimSpace(r.URL.Query().Get("conversation_id"))
	if len(resumedConvID) > 200 {
		slog.Warn("ws: conversation_id too long, ignoring", "len", len(resumedConvID))
		resumedConvID = ""
	}

	conn, err := w.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	// Read limits and deadlines.
	conn.SetReadLimit(wsMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
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

	// Ping ticker to detect dead connections. The goroutine below MUST exit
	// when the handler returns — `for range ticker.C` would block forever
	// because time.Ticker.Stop doesn't close the channel, so we gate the loop
	// on a done channel that the deferred cleanup closes.
	pingTicker := time.NewTicker(wsPingInterval)
	pingDone := make(chan struct{})
	defer func() {
		close(pingDone)
		pingTicker.Stop()
	}()
	go func() {
		for {
			select {
			case <-pingDone:
				return
			case <-pingTicker.C:
				wc.mu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				wc.mu.Unlock()
				if err != nil {
					return
				}
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

		// Continue-turn requests skip the text/attachment requirements — they
		// resume the existing conversation without adding a user message.
		if incoming.Type == "continue_turn" {
			if w.inbox == nil {
				slog.Warn("websocket: inbox not initialised, dropping continue_turn", "channel_id", connID)
				continue
			}
			select {
			case w.inbox <- IncomingMessage{
				ID:             uuid.New().String()[:8],
				ChannelID:      connID,
				SenderID:       incoming.SenderID,
				ConversationID: resumedConvID,
				Timestamp:      time.Now(),
				IsContinuation: true,
				Unlimited:      incoming.Unlimited,
			}:
			default:
				slog.Warn("websocket: inbox full, dropping continue_turn", "channel_id", connID)
			}
			continue
		}

		if incoming.Type != "message" {
			continue
		}

		// Require either text or at least one attachment.
		if incoming.Text == "" && len(incoming.Attachments) == 0 {
			continue
		}

		if w.inbox == nil {
			slog.Warn("websocket: inbox not initialised, dropping message", "channel_id", connID)
			continue
		}

		// Build the content.Blocks for this message.
		var blocks content.Blocks

		// Text block comes first (if any).
		if incoming.Text != "" {
			blocks = append(blocks, content.ContentBlock{
				Type: content.BlockText,
				Text: incoming.Text,
			})
		}

		// Resolve attachments.
		if len(incoming.Attachments) > 0 {
			if w.mediaStore == nil {
				// No media store — send a single error frame and fall through
				// to deliver any text-only content.
				_ = wc.writeJSON(wsMsg{
					Type: "error",
					Text: "media store not configured; attachments cannot be resolved",
				})
			} else {
				for _, att := range incoming.Attachments {
					data, mime, getErr := w.mediaStore.GetMedia(r.Context(), att.SHA256)
					if getErr != nil {
						slog.Warn("websocket: attachment not found", "sha256", att.SHA256, "channel_id", connID)
						_ = wc.writeJSON(wsMsg{
							Type: "error",
							Text: "attachment not found: " + att.SHA256,
						})
						continue
					}

					// For document MIMEs (PDF/DOCX/text/markdown) the providers
					// drop the bytes and substitute a placeholder string, so the
					// model never sees the file. Inline the extracted text as a
					// flagged BlockText instead — the model reads the content and
					// the agent's RAG layer skips this block when forming queries.
					blockType := content.BlockTypeFromMIME(mime)
					if blockType == content.BlockDocument && w.docExtractor != nil && w.docExtractor.Supports(mime) {
						extracted, exErr := w.docExtractor.Extract(r.Context(), data, mime)
						trimmed := strings.TrimSpace(extracted)
						if exErr == nil && trimmed != "" {
							label := att.Filename
							if label == "" {
								label = att.SHA256[:8]
							}
							inline := fmt.Sprintf(
								"[Attached file: %s — extracted text follows]\n\n%s\n\n[End of attached file: %s]",
								label, trimmed, label,
							)
							blocks = append(blocks, content.ContentBlock{
								Type:                    content.BlockText,
								Text:                    inline,
								ExtractedFromAttachment: true,
							})
							continue
						}
						slog.Warn("websocket: document text extraction failed, falling back to media block",
							"sha256", att.SHA256,
							"mime", mime,
							"err", exErr,
						)
					}

					blocks = append(blocks, content.ContentBlock{
						Type:        blockType,
						MediaSHA256: att.SHA256,
						MIME:        mime,
						Size:        att.Size,
						Filename:    att.Filename,
					})
				}
			}
		}

		// Skip if nothing survived (no text and all attachments failed).
		if len(blocks) == 0 {
			continue
		}

		msg := IncomingMessage{
			ID:             uuid.New().String()[:8],
			ChannelID:      connID,
			SenderID:       incoming.SenderID,
			ConversationID: resumedConvID,
			Content:        blocks,
			Timestamp:      time.Now(),
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

// WriteReasoning sends a reasoning_token frame to the WebSocket client.
// The payload uses "data" (not "text") to clearly distinguish reasoning
// fragments from regular text tokens.
func (sw *webStreamWriter) WriteReasoning(s string) error {
	return sw.wc.writeJSON(map[string]any{
		"type":       "reasoning_token",
		"data":       s,
		"channel_id": sw.channelID,
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
