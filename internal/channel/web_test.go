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
)

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
