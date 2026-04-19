package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"daimon/internal/config"
)

// tgAPICall records one call made to the mock Telegram API.
type tgAPICall struct {
	Method string
	ChatID int64
	MsgID  int
	Text   string
}

// mockTelegramServer creates an httptest server that mimics the Telegram Bot API
// for sendMessage and editMessageText. It returns the server, a function to
// retrieve recorded calls, and an optional error injector.
func mockTelegramServer(t *testing.T) (
	srv *httptest.Server,
	calls func() []tgAPICall,
	setError func(code int, description string),
) {
	t.Helper()

	var mu sync.Mutex
	var recorded []tgAPICall
	var errCode int
	var errDesc string
	nextMsgID := 100

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ec, ed := errCode, errDesc
		mu.Unlock()

		// Parse the path to get the method: /bot{token}/{method}
		parts := strings.Split(r.URL.Path, "/")
		method := parts[len(parts)-1]

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", 400)
			return
		}

		chatIDStr := r.FormValue("chat_id")
		text := r.FormValue("text")

		var chatID int64
		fmt.Sscanf(chatIDStr, "%d", &chatID)

		var msgID int
		if v := r.FormValue("message_id"); v != "" {
			fmt.Sscanf(v, "%d", &msgID)
		}

		mu.Lock()
		recorded = append(recorded, tgAPICall{
			Method: method,
			ChatID: chatID,
			MsgID:  msgID,
			Text:   text,
		})
		mu.Unlock()

		// Inject error if configured.
		if ec != 0 {
			resp := map[string]interface{}{
				"ok":          false,
				"error_code":  ec,
				"description": ed,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Success response.
		switch method {
		case "sendMessage":
			mu.Lock()
			id := nextMsgID
			nextMsgID++
			mu.Unlock()
			resp := map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"message_id": id,
					"chat":       map[string]interface{}{"id": chatID},
					"text":       text,
					"date":       time.Now().Unix(),
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case "editMessageText":
			resp := map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"message_id": msgID,
					"chat":       map[string]interface{}{"id": chatID},
					"text":       text,
					"date":       time.Now().Unix(),
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case "getMe":
			resp := map[string]interface{}{
				"ok": true,
				"result": map[string]interface{}{
					"id":         12345,
					"is_bot":     true,
					"first_name": "TestBot",
					"username":   "test_bot",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, "unknown method: "+method, 404)
		}
	}))

	calls = func() []tgAPICall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]tgAPICall, len(recorded))
		copy(out, recorded)
		return out
	}

	setError = func(code int, description string) {
		mu.Lock()
		defer mu.Unlock()
		errCode = code
		errDesc = description
	}

	return srv, calls, setError
}

// newMockTelegramChannel creates a TelegramChannel backed by the mock server.
func newMockTelegramChannel(t *testing.T, srvURL string) *TelegramChannel {
	t.Helper()

	// The telegram-bot-api library constructs URLs as:
	// https://api.telegram.org/bot{token}/{method}
	// We override the endpoint by using the mock server URL as the API endpoint.
	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint("test-token", srvURL+"/bot%s/%s")
	if err != nil {
		t.Fatalf("failed to create mock bot API: %v", err)
	}
	bot.Debug = false

	return &TelegramChannel{
		bot:       bot,
		config:    config.ChannelConfig{Token: "test-token"},
		whitelist: make(map[int64]bool),
	}
}

// TestTelegramChannel_ImplementsStreamSender verifies TelegramChannel satisfies StreamSender.
func TestTelegramChannel_ImplementsStreamSender(t *testing.T) {
	srv, _, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	var _ StreamSender = ch // compile-time assertion
}

// TestTelegramStream_BeginStream_CreatesInitialMessage verifies that BeginStream
// sends a placeholder "..." message via sendMessage.
func TestTelegramStream_BeginStream_CreatesInitialMessage(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}
	if sw == nil {
		t.Fatal("BeginStream() returned nil StreamWriter")
	}

	recorded := calls()
	// Filter for sendMessage calls (skip getMe from bot init).
	var sendCalls []tgAPICall
	for _, c := range recorded {
		if c.Method == "sendMessage" {
			sendCalls = append(sendCalls, c)
		}
	}

	if len(sendCalls) != 1 {
		t.Fatalf("expected 1 sendMessage call, got %d", len(sendCalls))
	}
	if sendCalls[0].ChatID != 42 {
		t.Errorf("sendMessage chat_id = %d, want 42", sendCalls[0].ChatID)
	}
	if sendCalls[0].Text != "..." {
		t.Errorf("sendMessage text = %q, want %q", sendCalls[0].Text, "...")
	}
}

// TestTelegramStream_BeginStream_InvalidChannelID verifies error on bad channel ID.
func TestTelegramStream_BeginStream_InvalidChannelID(t *testing.T) {
	srv, _, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	_, err := ch.BeginStream(context.Background(), "telegram:not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid channel ID, got nil")
	}
}

// TestTelegramStream_WriteChunk_BuffersAndThrottles verifies that rapid WriteChunk
// calls are buffered and only periodic edits are sent.
func TestTelegramStream_WriteChunk_BuffersAndThrottles(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	// The first WriteChunk should trigger an edit because lastFlush is zero.
	if err := sw.WriteChunk("Hello"); err != nil {
		t.Fatalf("WriteChunk(1) error: %v", err)
	}

	// Subsequent rapid WriteChunks should be buffered (not flushed) since
	// less than 1 second has elapsed.
	if err := sw.WriteChunk(" world"); err != nil {
		t.Fatalf("WriteChunk(2) error: %v", err)
	}
	if err := sw.WriteChunk("!"); err != nil {
		t.Fatalf("WriteChunk(3) error: %v", err)
	}

	// Count editMessageText calls (excluding sendMessage and getMe).
	recorded := calls()
	var editCount int
	for _, c := range recorded {
		if c.Method == "editMessageText" {
			editCount++
		}
	}

	// Only 1 edit should have been sent (from the first WriteChunk).
	if editCount != 1 {
		t.Errorf("expected 1 editMessageText call (throttled), got %d", editCount)
	}
}

// TestTelegramStream_Finalize_FlushesAll verifies that Finalize sends the
// complete accumulated text as a final edit.
func TestTelegramStream_Finalize_FlushesAll(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	// Write multiple chunks rapidly (first triggers immediate edit, rest buffered).
	_ = sw.WriteChunk("Hello")
	_ = sw.WriteChunk(" world")
	_ = sw.WriteChunk("!")

	// Finalize should flush the complete accumulated text.
	if err := sw.Finalize(); err != nil {
		t.Fatalf("Finalize() error: %v", err)
	}

	recorded := calls()
	var edits []tgAPICall
	for _, c := range recorded {
		if c.Method == "editMessageText" {
			edits = append(edits, c)
		}
	}

	if len(edits) < 1 {
		t.Fatal("expected at least 1 editMessageText call after Finalize")
	}

	// The last edit should contain the full text.
	lastEdit := edits[len(edits)-1]
	if lastEdit.Text != "Hello world!" {
		t.Errorf("final editMessageText text = %q, want %q", lastEdit.Text, "Hello world!")
	}
}

// TestTelegramStream_Abort_SendsPartialContent verifies that Abort flushes
// accumulated text plus an error indicator.
func TestTelegramStream_Abort_SendsPartialContent(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	_ = sw.WriteChunk("Partial response")
	if err := sw.Abort(fmt.Errorf("connection lost")); err != nil {
		t.Fatalf("Abort() error: %v", err)
	}

	recorded := calls()
	var edits []tgAPICall
	for _, c := range recorded {
		if c.Method == "editMessageText" {
			edits = append(edits, c)
		}
	}

	if len(edits) < 1 {
		t.Fatal("expected at least 1 editMessageText call after Abort")
	}

	lastEdit := edits[len(edits)-1]
	if !strings.Contains(lastEdit.Text, "Partial response") {
		t.Errorf("abort edit should contain original text, got %q", lastEdit.Text)
	}
	if !strings.Contains(lastEdit.Text, "[Error: connection lost]") {
		t.Errorf("abort edit should contain error indicator, got %q", lastEdit.Text)
	}
}

// TestTelegramStream_Throttling_OnlyPeriodicEdits verifies that when enough
// time passes between WriteChunk calls, each one triggers an edit.
func TestTelegramStream_Throttling_OnlyPeriodicEdits(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	// Access the underlying writer to reduce the interval for testing.
	tw := sw.(*telegramStreamWriter)
	tw.minInterval = 10 * time.Millisecond

	// First chunk — triggers immediate edit (lastFlush is zero).
	if err := tw.WriteChunk("A"); err != nil {
		t.Fatalf("WriteChunk error: %v", err)
	}

	// Wait for the interval to pass.
	time.Sleep(20 * time.Millisecond)

	// Second chunk — should trigger another edit.
	if err := tw.WriteChunk("B"); err != nil {
		t.Fatalf("WriteChunk error: %v", err)
	}

	// Wait again.
	time.Sleep(20 * time.Millisecond)

	// Third chunk — should trigger yet another edit.
	if err := tw.WriteChunk("C"); err != nil {
		t.Fatalf("WriteChunk error: %v", err)
	}

	recorded := calls()
	var editCount int
	for _, c := range recorded {
		if c.Method == "editMessageText" {
			editCount++
		}
	}

	// All 3 chunks should have triggered edits since we waited between each.
	if editCount != 3 {
		t.Errorf("expected 3 editMessageText calls (spaced out), got %d", editCount)
	}
}

// TestTelegramStream_RateLimitBackoff verifies that a 429 response causes
// the writer to increase its minimum interval.
func TestTelegramStream_RateLimitBackoff(t *testing.T) {
	srv, _, setError := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	tw := sw.(*telegramStreamWriter)
	originalInterval := tw.minInterval

	// Inject a 429 error for subsequent calls.
	setError(429, "Too Many Requests: retry after 1")

	// Write a chunk — it will attempt to flush and hit the 429.
	if err := tw.WriteChunk("throttled"); err != nil {
		t.Fatalf("WriteChunk() should not return error on 429, got: %v", err)
	}

	// The interval should have been doubled.
	if tw.minInterval <= originalInterval {
		t.Errorf("expected minInterval to increase after 429, got %v (was %v)",
			tw.minInterval, originalInterval)
	}
}

// TestTelegramStream_MessageNotModified_Ignored verifies that the
// "message is not modified" error from Telegram is silently ignored.
func TestTelegramStream_MessageNotModified_Ignored(t *testing.T) {
	srv, _, setError := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	// Inject the "not modified" error.
	setError(400, "Bad Request: message is not modified")

	tw := sw.(*telegramStreamWriter)
	if err := tw.WriteChunk("same content"); err != nil {
		t.Fatalf("WriteChunk() should not return error on 'not modified', got: %v", err)
	}
}

// TestTelegramStream_LongMessage_Truncated verifies that messages exceeding
// 4096 characters are truncated during streaming flushes.
func TestTelegramStream_LongMessage_Truncated(t *testing.T) {
	srv, calls, _ := mockTelegramServer(t)
	defer srv.Close()

	ch := newMockTelegramChannel(t, srv.URL)
	sw, err := ch.BeginStream(context.Background(), "telegram:42")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	// Write a chunk that exceeds 4096 characters.
	longText := strings.Repeat("X", 5000)
	_ = sw.WriteChunk(longText)
	_ = sw.Finalize()

	recorded := calls()
	var edits []tgAPICall
	for _, c := range recorded {
		if c.Method == "editMessageText" {
			edits = append(edits, c)
		}
	}

	if len(edits) < 1 {
		t.Fatal("expected at least 1 editMessageText call")
	}

	lastEdit := edits[len(edits)-1]
	if len([]rune(lastEdit.Text)) > 4096 {
		t.Errorf("edited message length = %d runes, want <= 4096", len([]rune(lastEdit.Text)))
	}
}
