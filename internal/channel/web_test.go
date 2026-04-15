package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"microagent/internal/content"
	"microagent/internal/store"
)

// mockMediaStore implements store.MediaStore for testing.
type mockMediaStore struct {
	blobs map[string][]byte
	mimes map[string]string
}

func newMockMediaStore() *mockMediaStore {
	return &mockMediaStore{
		blobs: make(map[string][]byte),
		mimes: make(map[string]string),
	}
}

func (m *mockMediaStore) StoreMedia(_ context.Context, data []byte, mime string) (string, error) {
	sha := fmt.Sprintf("sha-%d", len(data))
	m.blobs[sha] = data
	m.mimes[sha] = mime
	return sha, nil
}

func (m *mockMediaStore) GetMedia(_ context.Context, sha256 string) ([]byte, string, error) {
	data, ok := m.blobs[sha256]
	if !ok {
		return nil, "", store.ErrMediaNotFound
	}
	return data, m.mimes[sha256], nil
}

func (m *mockMediaStore) TouchMedia(_ context.Context, sha256 string) error {
	if _, ok := m.blobs[sha256]; !ok {
		return store.ErrMediaNotFound
	}
	return nil
}

func (m *mockMediaStore) PruneUnreferencedMedia(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

func (m *mockMediaStore) ListMedia(_ context.Context) ([]store.MediaMeta, error) {
	return nil, nil
}

func (m *mockMediaStore) DeleteMedia(_ context.Context, _ string) error {
	return nil
}

// newTestServerWithMediaStore creates a WebChannel with a MediaStore wired in.
func newTestServerWithMediaStore(t *testing.T, ms store.MediaStore) (*httptest.Server, *WebChannel, <-chan IncomingMessage) {
	t.Helper()
	wc := NewWebChannel()
	wc.SetMediaStore(ms)
	inbox := make(chan IncomingMessage, 16)
	if err := wc.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/chat", wc.HandleWebSocket)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		_ = wc.Stop()
	})
	return srv, wc, inbox
}

// dialWS upgrades an httptest.Server URL to a WebSocket connection.
func dialWS(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/chat"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

// readJSON reads one TextMessage from conn and unmarshals into v.
func readJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, b, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, b)
	}
}

// newTestServer creates a WebChannel, starts it with a buffered inbox, mounts the
// handler, and returns the server + channel + inbox.
func newTestServer(t *testing.T) (*httptest.Server, *WebChannel, <-chan IncomingMessage) {
	t.Helper()
	wc := NewWebChannel()
	inbox := make(chan IncomingMessage, 16)
	if err := wc.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/chat", wc.HandleWebSocket)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		_ = wc.Stop()
	})
	return srv, wc, inbox
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWebChannel_Name(t *testing.T) {
	wc := NewWebChannel()
	if wc.Name() != "web" {
		t.Errorf("Name() = %q, want %q", wc.Name(), "web")
	}
}

func TestWebChannel_UpgradeAndReceiveMessage(t *testing.T) {
	srv, _, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	// Send a user message.
	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "hello agent", SenderID: "tester"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The channel should push an IncomingMessage to the inbox.
	select {
	case msg := <-inbox:
		if msg.Text() != "hello agent" {
			t.Errorf("Text() = %q, want %q", msg.Text(), "hello agent")
		}
		if msg.SenderID != "tester" {
			t.Errorf("SenderID = %q, want %q", msg.SenderID, "tester")
		}
		if !strings.HasPrefix(msg.ChannelID, "web:") {
			t.Errorf("ChannelID = %q, want prefix %q", msg.ChannelID, "web:")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}
}

func TestWebChannel_SendToClient(t *testing.T) {
	srv, wc, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	// Trigger a message so we learn the channelID.
	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "ping"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	var msg IncomingMessage
	select {
	case msg = <-inbox:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}

	// Now send a reply via Send().
	outMsg := OutgoingMessage{ChannelID: msg.ChannelID, Text: "pong"}
	if err := wc.Send(context.Background(), outMsg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got wsMsg
	readJSON(t, conn, &got)
	if got.Type != "message" {
		t.Errorf("type = %q, want %q", got.Type, "message")
	}
	if got.Text != "pong" {
		t.Errorf("text = %q, want %q", got.Text, "pong")
	}
	if got.ChannelID != msg.ChannelID {
		t.Errorf("channel_id = %q, want %q", got.ChannelID, msg.ChannelID)
	}
}

func TestWebChannel_Streaming(t *testing.T) {
	srv, wc, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	// Trigger so we get a channelID.
	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "stream me"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	var msg IncomingMessage
	select {
	case msg = <-inbox:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}

	sw, err := wc.BeginStream(context.Background(), msg.ChannelID)
	if err != nil {
		t.Fatalf("BeginStream: %v", err)
	}

	chunks := []string{"Hello", " ", "world"}
	for _, chunk := range chunks {
		if err := sw.WriteChunk(chunk); err != nil {
			t.Fatalf("WriteChunk: %v", err)
		}
	}
	if err := sw.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Read chunk frames.
	for _, want := range chunks {
		var got wsMsg
		readJSON(t, conn, &got)
		if got.Type != "token" {
			t.Errorf("type = %q, want stream_chunk", got.Type)
		}
		if got.Text != want {
			t.Errorf("text = %q, want %q", got.Text, want)
		}
	}

	// Read stream_end.
	var end wsMsg
	readJSON(t, conn, &end)
	if end.Type != "done" {
		t.Errorf("type = %q, want stream_end", end.Type)
	}
}

func TestWebChannel_StreamAbort(t *testing.T) {
	srv, wc, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "abort me"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	var msg IncomingMessage
	select {
	case msg = <-inbox:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}

	sw, err := wc.BeginStream(context.Background(), msg.ChannelID)
	if err != nil {
		t.Fatalf("BeginStream: %v", err)
	}
	_ = sw.WriteChunk("partial")

	abortErr := fmt.Errorf("something went wrong")
	if err := sw.Abort(abortErr); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	// Read the chunk.
	var chunk wsMsg
	readJSON(t, conn, &chunk)
	if chunk.Type != "token" {
		t.Errorf("type = %q, want stream_chunk", chunk.Type)
	}

	// Read the error frame.
	var errFrame wsMsg
	readJSON(t, conn, &errFrame)
	if errFrame.Type != "error" {
		t.Errorf("type = %q, want error", errFrame.Type)
	}
	if errFrame.Message != "something went wrong" {
		t.Errorf("message = %q, want %q", errFrame.Message, "something went wrong")
	}
}

func TestWebChannel_ConnectionCleanupOnClose(t *testing.T) {
	srv, wc, inbox := newTestServer(t)

	conn := dialWS(t, srv)

	// Trigger so we know the channelID.
	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "bye"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	var msg IncomingMessage
	select {
	case msg = <-inbox:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}

	channelID := msg.ChannelID

	// Verify connection exists.
	if _, ok := wc.conns.Load(channelID); !ok {
		t.Fatal("expected connection to be registered")
	}

	// Close client connection.
	conn.Close()

	// Wait for server-side cleanup.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := wc.conns.Load(channelID); !ok {
			return // cleaned up
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("connection not cleaned up after close")
}

func TestWebChannel_BeginStream_UnknownChannelID(t *testing.T) {
	wc := NewWebChannel()
	_, err := wc.BeginStream(context.Background(), "web:doesnotexist")
	if err == nil {
		t.Fatal("expected error for unknown channelID")
	}
}

func TestWebChannel_Send_UnknownChannelID(t *testing.T) {
	wc := NewWebChannel()
	err := wc.Send(context.Background(), OutgoingMessage{ChannelID: "web:doesnotexist", Text: "hi"})
	if err == nil {
		t.Fatal("expected error for unknown channelID")
	}
}

func TestWebChannel_EmitTelemetry(t *testing.T) {
	srv, wc, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	// Trigger so we learn the channelID.
	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "ping"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	var msg IncomingMessage
	select {
	case msg = <-inbox:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}

	// EmitTelemetry should succeed and the frame should arrive on the client.
	frame := map[string]any{
		"type":         "tool_start",
		"name":         "shell_exec",
		"tool_call_id": "tc_001",
	}
	if err := wc.EmitTelemetry(context.Background(), msg.ChannelID, frame); err != nil {
		t.Fatalf("EmitTelemetry: %v", err)
	}

	var got map[string]any
	readJSON(t, conn, &got)
	if got["type"] != "tool_start" {
		t.Errorf("type = %v, want tool_start", got["type"])
	}
	if got["name"] != "shell_exec" {
		t.Errorf("name = %v, want shell_exec", got["name"])
	}
	if got["channel_id"] != msg.ChannelID {
		t.Errorf("channel_id = %v, want %v", got["channel_id"], msg.ChannelID)
	}
}

func TestWebChannel_EmitTelemetry_UnknownChannelID(t *testing.T) {
	wc := NewWebChannel()
	// Should return nil silently for unknown channel.
	err := wc.EmitTelemetry(context.Background(), "web:doesnotexist", map[string]any{"type": "status"})
	if err != nil {
		t.Fatalf("expected nil for unknown channelID, got: %v", err)
	}
}

func TestWebChannel_IgnoresNonMessageTypes(t *testing.T) {
	srv, _, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	// Send a non-"message" type — should be silently ignored.
	payload, _ := json.Marshal(wsMsg{Type: "ping", Text: "keepalive"})
	_ = conn.WriteMessage(websocket.TextMessage, payload)

	// Then send a real message.
	payload2, _ := json.Marshal(wsMsg{Type: "message", Text: "real message"})
	_ = conn.WriteMessage(websocket.TextMessage, payload2)

	select {
	case msg := <-inbox:
		if msg.Text() != "real message" {
			t.Errorf("got %q, want %q", msg.Text(), "real message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for real message")
	}
}

// ---------------------------------------------------------------------------
// wsAttachment + wsMsg.Attachments
// ---------------------------------------------------------------------------

func TestWsMsg_Attachments_OmitEmpty(t *testing.T) {
	msg := wsMsg{Type: "message", Text: "hello"}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["attachments"]; ok {
		t.Errorf("expected no 'attachments' key when attachments is nil, got: %v", m)
	}
}

func TestWsMsg_Attachments_WithData(t *testing.T) {
	msg := wsMsg{
		Type: "message",
		Text: "with attachment",
		Attachments: []wsAttachment{
			{
				SHA256:   "abc123",
				MIME:     "image/png",
				Size:     1024,
				Filename: "photo.png",
			},
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got wsMsg
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got.Attachments))
	}
	a := got.Attachments[0]
	if a.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want %q", a.SHA256, "abc123")
	}
	if a.MIME != "image/png" {
		t.Errorf("MIME = %q, want %q", a.MIME, "image/png")
	}
	if a.Size != 1024 {
		t.Errorf("Size = %d, want %d", a.Size, 1024)
	}
	if a.Filename != "photo.png" {
		t.Errorf("Filename = %q, want %q", a.Filename, "photo.png")
	}
}

// ---------------------------------------------------------------------------
// T3.1 — HandleWebSocket attachment resolution
// ---------------------------------------------------------------------------

// TestWebChannel_TextOnly_BackwardCompat verifies that a text-only message
// (no attachments field) produces a single text block — identical to the
// pre-attachment behavior. T4.2 regression test.
func TestWebChannel_TextOnly_BackwardCompat(t *testing.T) {
	srv, _, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{Type: "message", Text: "hello"})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case msg := <-inbox:
		if len(msg.Content) != 1 {
			t.Fatalf("expected 1 block, got %d", len(msg.Content))
		}
		if msg.Content[0].Type != content.BlockText {
			t.Errorf("block type = %q, want %q", msg.Content[0].Type, content.BlockText)
		}
		if msg.Content[0].Text != "hello" {
			t.Errorf("text = %q, want %q", msg.Content[0].Text, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}
}

// TestWebChannel_TextPlusValidAttachment verifies text + attachment produces
// a Blocks slice with [text, image].
func TestWebChannel_TextPlusValidAttachment(t *testing.T) {
	ms := newMockMediaStore()
	ms.blobs["sha256abc"] = []byte("fake image bytes")
	ms.mimes["sha256abc"] = "image/png"

	srv, _, inbox := newTestServerWithMediaStore(t, ms)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{
		Type: "message",
		Text: "look at this",
		Attachments: []wsAttachment{
			{SHA256: "sha256abc", MIME: "image/png", Size: 100, Filename: "photo.png"},
		},
	})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case msg := <-inbox:
		if len(msg.Content) != 2 {
			t.Fatalf("expected 2 blocks, got %d: %+v", len(msg.Content), msg.Content)
		}
		if msg.Content[0].Type != content.BlockText {
			t.Errorf("block[0].Type = %q, want text", msg.Content[0].Type)
		}
		if msg.Content[0].Text != "look at this" {
			t.Errorf("block[0].Text = %q, want %q", msg.Content[0].Text, "look at this")
		}
		if msg.Content[1].Type != content.BlockImage {
			t.Errorf("block[1].Type = %q, want image", msg.Content[1].Type)
		}
		if msg.Content[1].MediaSHA256 != "sha256abc" {
			t.Errorf("block[1].MediaSHA256 = %q, want sha256abc", msg.Content[1].MediaSHA256)
		}
		if msg.Content[1].MIME != "image/png" {
			t.Errorf("block[1].MIME = %q, want image/png", msg.Content[1].MIME)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}
}

// TestWebChannel_AttachmentOnly verifies that an attachment with no text
// produces a Blocks slice with just the image block.
func TestWebChannel_AttachmentOnly(t *testing.T) {
	ms := newMockMediaStore()
	ms.blobs["sha256doc"] = []byte("file bytes")
	ms.mimes["sha256doc"] = "application/pdf"

	srv, _, inbox := newTestServerWithMediaStore(t, ms)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{
		Type: "message",
		Attachments: []wsAttachment{
			{SHA256: "sha256doc", MIME: "application/pdf", Size: 200, Filename: "doc.pdf"},
		},
	})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case msg := <-inbox:
		if len(msg.Content) != 1 {
			t.Fatalf("expected 1 block, got %d", len(msg.Content))
		}
		if msg.Content[0].Type != content.BlockDocument {
			t.Errorf("block[0].Type = %q, want document", msg.Content[0].Type)
		}
		if msg.Content[0].MediaSHA256 != "sha256doc" {
			t.Errorf("block[0].MediaSHA256 = %q, want sha256doc", msg.Content[0].MediaSHA256)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for inbox message")
	}
}

// TestWebChannel_InvalidSHA_ErrorFrame verifies that an unknown SHA-256 sends
// an error frame to the client. If text exists, message is still delivered.
func TestWebChannel_InvalidSHA_ErrorFrame(t *testing.T) {
	ms := newMockMediaStore() // empty — no blobs registered

	srv, _, inbox := newTestServerWithMediaStore(t, ms)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{
		Type: "message",
		Text: "with bad attachment",
		Attachments: []wsAttachment{
			{SHA256: "notexist", MIME: "image/png", Size: 10},
		},
	})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Expect an error frame from the server.
	var errFrame wsMsg
	readJSON(t, conn, &errFrame)
	if errFrame.Type != "error" {
		t.Errorf("expected error frame, got type=%q", errFrame.Type)
	}
	if !strings.Contains(errFrame.Text, "notexist") {
		t.Errorf("error frame text should mention sha256, got: %q", errFrame.Text)
	}

	// Text block should still be delivered since text was present.
	select {
	case msg := <-inbox:
		if msg.Content.TextOnly() != "with bad attachment" {
			t.Errorf("text = %q, want %q", msg.Content.TextOnly(), "with bad attachment")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: expected text-only message after attachment failure")
	}
}

// TestWebChannel_NilMediaStore_Attachments verifies that when mediaStore is nil
// and attachments are present, an error frame is sent and text is still processed.
func TestWebChannel_NilMediaStore_Attachments(t *testing.T) {
	// Use regular newTestServer (no media store).
	srv, _, inbox := newTestServer(t)

	conn := dialWS(t, srv)
	defer conn.Close()

	payload, _ := json.Marshal(wsMsg{
		Type: "message",
		Text: "has attachment but no store",
		Attachments: []wsAttachment{
			{SHA256: "sha256abc", MIME: "image/png", Size: 100},
		},
	})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Expect an error frame.
	var errFrame wsMsg
	readJSON(t, conn, &errFrame)
	if errFrame.Type != "error" {
		t.Errorf("expected error frame, got type=%q text=%q", errFrame.Type, errFrame.Text)
	}

	// Text should still reach inbox.
	select {
	case msg := <-inbox:
		if msg.Content.TextOnly() != "has attachment but no store" {
			t.Errorf("text = %q, want %q", msg.Content.TextOnly(), "has attachment but no store")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: expected text-only message after nil mediaStore")
	}
}
