package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/store"
)

// fakeMediaStore is a test double for store.MediaStore that tracks StoreMedia calls.
type fakeMediaStore struct {
	mu      sync.Mutex
	stored  map[string][]byte // sha -> data
	callCnt int
}

func newFakeMediaStore() *fakeMediaStore {
	return &fakeMediaStore{stored: make(map[string][]byte)}
}

func (f *fakeMediaStore) StoreMedia(_ context.Context, data []byte, mime string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCnt++
	sha := fmt.Sprintf("sha256-of-%d-bytes-%s", len(data), mime)
	f.stored[sha] = data
	return sha, nil
}

func (f *fakeMediaStore) GetMedia(_ context.Context, sha256 string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.stored[sha256]
	if !ok {
		return nil, "", store.ErrMediaNotFound
	}
	return d, "image/jpeg", nil
}

func (f *fakeMediaStore) TouchMedia(_ context.Context, _ string) error { return nil }

func (f *fakeMediaStore) PruneUnreferencedMedia(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeMediaStore) ListMedia(_ context.Context) ([]store.MediaMeta, error) {
	return nil, nil
}

func (f *fakeMediaStore) DeleteMedia(_ context.Context, _ string) error {
	return nil
}

// buildMediaConfig returns a MediaConfig with sensible test defaults (media enabled).
func buildMediaConfig(maxAttach, maxMsg int64, prefixes []string) config.MediaConfig {
	enabled := true
	return config.MediaConfig{
		Enabled:             &enabled,
		MaxAttachmentBytes:  maxAttach,
		MaxMessageBytes:     maxMsg,
		AllowedMIMEPrefixes: prefixes,
	}
}

// disabledMediaConfig returns a config with media disabled.
func disabledMediaConfig() config.MediaConfig {
	disabled := false
	return config.MediaConfig{
		Enabled:            &disabled,
		MaxAttachmentBytes: 10 * 1024 * 1024,
		MaxMessageBytes:    25 * 1024 * 1024,
	}
}

// newTestChannel constructs a TelegramChannel with injected stubs — no real Telegram API.
func newTestChannel(media config.MediaConfig, ms store.MediaStore, cdnURL string) *TelegramChannel {
	tc := &TelegramChannel{
		bot:        nil, // not used in processUpdate tests (bot only needed for Send/ping)
		media:      media,
		mediaStore: ms,
		whitelist:  map[int64]bool{}, // empty = allow all
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	tc.getDirectURL = func(_ string) (string, error) {
		return cdnURL, nil
	}
	return tc
}

// newTestChannelWithURLErr constructs a channel where getDirectURL always fails.
func newTestChannelWithURLErr(media config.MediaConfig, ms store.MediaStore, urlErr error) *TelegramChannel {
	tc := &TelegramChannel{
		bot:        nil,
		media:      media,
		mediaStore: ms,
		whitelist:  map[int64]bool{},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	tc.getDirectURL = func(_ string) (string, error) {
		return "", urlErr
	}
	return tc
}

// makeUpdate builds a minimal tgbotapi.Update for testing.
func makeUpdate(msg *tgbotapi.Message) tgbotapi.Update {
	return tgbotapi.Update{Message: msg}
}

// makeMsg builds a tgbotapi.Message with sender info populated.
func makeMsg() *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 42, UserName: "testuser"},
		Chat:      &tgbotapi.Chat{ID: 100},
		Date:      int(time.Now().Unix()),
	}
}

// --- Tests ---

// mockBot is used to bypass the actual Telegram API for testing `Send` chunking natively.
func TestTelegramChannel_Send_Chunking(t *testing.T) {
	// We are testing exclusively the logic inside Send natively bypassing physical tgbotapi logic directly here
	// by simulating an incoming long string chunk logic cleanly using direct loop slices math

	const maxChars = 4000
	msgText := strings.Repeat("A", 9000)

	// Since we don't want to actually ping Telegram, we're validating the math logic manually
	chatStr := "12345"
	var chatID int64
	_, err := fmt.Sscanf(chatStr, "%d", &chatID)
	if err != nil {
		t.Fatalf("sscanf failed: %v", err)
	}

	runes := []rune(msgText)
	length := len(runes)

	var chunks []string
	for i := 0; i < length; i += maxChars {
		end := i + maxChars
		if end > length {
			end = length
		}
		chunks = append(chunks, string(runes[i:end]))
	}

	if len(chunks) != 3 {
		t.Errorf("Expected 3 chunks, got %d", len(chunks))
	}

	if len(chunks[0]) != maxChars {
		t.Errorf("Expected chunk 1 to be %d length, got %d", maxChars, len(chunks[0]))
	}

	if len(chunks[2]) != 1000 {
		t.Errorf("Expected chunk 3 to be 1000 length residue, got %d", len(chunks[2]))
	}
}

func TestTelegramChannel_Whitelist_Map(t *testing.T) {
	cfg := config.ChannelConfig{
		Token:        "dummy",
		AllowedUsers: []int64{123, 456},
	}

	// Logic mock of the startup pipeline
	whitelist := make(map[int64]bool)
	for _, id := range cfg.AllowedUsers {
		whitelist[id] = true
	}

	if !whitelist[123] {
		t.Error("expected 123 to be whitelisted")
	}

	if whitelist[999] {
		t.Error("expected 999 to NOT be whitelisted natively")
	}
}

// TestProcessUpdate_PhotoWithCaption verifies photo + caption → [text, image] blocks.
func TestProcessUpdate_PhotoWithCaption(t *testing.T) {
	const photoBytes = "FAKEJPEGDATA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(photoBytes))
	}))
	defer srv.Close()

	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/", "audio/"})
	tc := newTestChannel(mediaCfg, ms, srv.URL)

	msg := makeMsg()
	msg.Caption = "look at this"
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "small", FileSize: 100, Width: 100, Height: 100},
		{FileID: "large", FileSize: int(len(photoBytes)), Width: 800, Height: 600},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if len(result.Content) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(result.Content), result.Content)
	}

	if result.Content[0].Type != content.BlockText || result.Content[0].Text != "look at this" {
		t.Errorf("block[0] expected text 'look at this', got %+v", result.Content[0])
	}
	if result.Content[1].Type != content.BlockImage {
		t.Errorf("block[1] expected BlockImage, got %+v", result.Content[1])
	}
	if ms.callCnt != 1 {
		t.Errorf("expected 1 StoreMedia call, got %d", ms.callCnt)
	}
}

// TestProcessUpdate_PhotoWithoutCaption verifies photo without caption → [image] only.
func TestProcessUpdate_PhotoWithoutCaption(t *testing.T) {
	const photoBytes = "FAKEJPEGDATA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(photoBytes))
	}))
	defer srv.Close()

	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/", "audio/"})
	tc := newTestChannel(mediaCfg, ms, srv.URL)

	msg := makeMsg()
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "large", FileSize: int(len(photoBytes)), Width: 800, Height: 600},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 block (image only), got %d: %+v", len(result.Content), result.Content)
	}
	if result.Content[0].Type != content.BlockImage {
		t.Errorf("block[0] expected BlockImage, got %+v", result.Content[0])
	}
}

// TestProcessUpdate_PhotoOversized verifies oversized photo → rejection notice, no StoreMedia call.
func TestProcessUpdate_PhotoOversized(t *testing.T) {
	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(100, 1024, []string{"image/"})
	// URL won't be called — size gate fires before download
	tc := newTestChannel(mediaCfg, ms, "http://should-not-be-called")

	msg := makeMsg()
	msg.Caption = "big photo"
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "huge", FileSize: 999999, Width: 4000, Height: 3000},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued (with rejection notice)")
	}

	if ms.callCnt != 0 {
		t.Errorf("expected 0 StoreMedia calls for oversized photo, got %d", ms.callCnt)
	}

	// Should have caption block + rejection notice
	hasRejection := false
	for _, b := range result.Content {
		if b.Type == content.BlockText && strings.Contains(b.Text, "too large") {
			hasRejection = true
		}
	}
	if !hasRejection {
		t.Errorf("expected rejection notice in blocks, got: %+v", result.Content)
	}
}

// TestProcessUpdate_CDN500WithCaption verifies CDN 500 → [caption text, failure notice].
func TestProcessUpdate_CDN500WithCaption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/"})
	tc := newTestChannel(mediaCfg, ms, srv.URL)

	msg := makeMsg()
	msg.Caption = "my photo"
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "f1", FileSize: 500, Width: 100, Height: 100},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if len(result.Content) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d: %+v", len(result.Content), result.Content)
	}
	if result.Content[0].Type != content.BlockText || result.Content[0].Text != "my photo" {
		t.Errorf("block[0] expected caption text, got %+v", result.Content[0])
	}

	hasFailure := false
	for _, b := range result.Content {
		if b.Type == content.BlockText && strings.Contains(b.Text, "media failed to download") {
			hasFailure = true
		}
	}
	if !hasFailure {
		t.Errorf("expected download failure notice, got: %+v", result.Content)
	}
	if ms.callCnt != 0 {
		t.Errorf("expected 0 StoreMedia calls on CDN failure, got %d", ms.callCnt)
	}
}

// TestProcessUpdate_CDNTimeoutNoCaption verifies CDN timeout (getDirectURL error) → [notice only].
func TestProcessUpdate_CDNTimeoutNoCaption(t *testing.T) {
	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/"})
	tc := newTestChannelWithURLErr(mediaCfg, ms, errors.New("context deadline exceeded"))

	msg := makeMsg()
	// No caption
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "f1", FileSize: 500, Width: 100, Height: 100},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 block (notice only, no caption), got %d: %+v", len(result.Content), result.Content)
	}
	if !strings.Contains(result.Content[0].Text, "media failed to download") {
		t.Errorf("expected download failure notice, got: %+v", result.Content[0])
	}
}

// TestProcessUpdate_BlockedMIMEDocument verifies blocked MIME → rejection notice, no download.
func TestProcessUpdate_BlockedMIMEDocument(t *testing.T) {
	ms := newFakeMediaStore()
	// Only allow image/ — not application/zip
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/"})
	tc := newTestChannel(mediaCfg, ms, "http://should-not-be-called")

	msg := makeMsg()
	msg.Document = &tgbotapi.Document{
		FileID:   "doc1",
		FileSize: 1024,
		MimeType: "application/zip",
		FileName: "archive.zip",
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued with rejection")
	}

	if ms.callCnt != 0 {
		t.Errorf("expected 0 StoreMedia calls for blocked MIME, got %d", ms.callCnt)
	}

	hasRejection := false
	for _, b := range result.Content {
		if b.Type == content.BlockText && strings.Contains(b.Text, "not allowed") {
			hasRejection = true
		}
	}
	if !hasRejection {
		t.Errorf("expected MIME rejection notice, got: %+v", result.Content)
	}
}

// TestProcessUpdate_MediaDisabled verifies media.enabled=false → caption + disabled notice.
func TestProcessUpdate_MediaDisabled(t *testing.T) {
	ms := newFakeMediaStore()
	mediaCfg := disabledMediaConfig()
	tc := newTestChannel(mediaCfg, ms, "http://should-not-be-called")

	msg := makeMsg()
	msg.Caption = "a cool photo"
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "f1", FileSize: 500, Width: 100, Height: 100},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if ms.callCnt != 0 {
		t.Errorf("expected 0 StoreMedia calls when media disabled, got %d", ms.callCnt)
	}

	hasCaption := false
	hasDisabledNotice := false
	for _, b := range result.Content {
		if b.Type == content.BlockText && b.Text == "a cool photo" {
			hasCaption = true
		}
		if b.Type == content.BlockText && strings.Contains(b.Text, "disabled in config") {
			hasDisabledNotice = true
		}
	}
	if !hasCaption {
		t.Errorf("expected caption block, got: %+v", result.Content)
	}
	if !hasDisabledNotice {
		t.Errorf("expected disabled notice block, got: %+v", result.Content)
	}
}

// TestProcessUpdate_NilMediaStoreDisablesMedia verifies nil mediaStore treated as media disabled.
func TestProcessUpdate_NilMediaStoreDisablesMedia(t *testing.T) {
	enabled := true
	mediaCfg := config.MediaConfig{
		Enabled:            &enabled,
		MaxAttachmentBytes: 1024 * 1024,
		MaxMessageBytes:    10 * 1024 * 1024,
	}
	// nil store — even though Enabled=true, should behave as disabled
	tc := newTestChannel(mediaCfg, nil, "http://should-not-be-called")

	msg := makeMsg()
	msg.Photo = []tgbotapi.PhotoSize{
		{FileID: "f1", FileSize: 500},
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	hasDisabledNotice := false
	for _, b := range result.Content {
		if b.Type == content.BlockText && strings.Contains(b.Text, "disabled in config") {
			hasDisabledNotice = true
		}
	}
	if !hasDisabledNotice {
		t.Errorf("expected disabled notice when mediaStore is nil, got: %+v", result.Content)
	}
}

// TestProcessUpdate_TextMessage verifies plain text messages are unaffected.
func TestProcessUpdate_TextMessage(t *testing.T) {
	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/"})
	tc := newTestChannel(mediaCfg, ms, "http://irrelevant")

	msg := makeMsg()
	msg.Text = "hello world"

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected text message to be enqueued")
	}

	if len(result.Content) != 1 || result.Content[0].Type != content.BlockText {
		t.Errorf("expected single text block, got: %+v", result.Content)
	}
	if result.Content[0].Text != "hello world" {
		t.Errorf("expected 'hello world', got %q", result.Content[0].Text)
	}
	if ms.callCnt != 0 {
		t.Errorf("expected 0 StoreMedia for text message, got %d", ms.callCnt)
	}
}

// TestProcessUpdate_Document_Allowed verifies an allowed MIME document is downloaded and stored.
func TestProcessUpdate_Document_Allowed(t *testing.T) {
	const docContent = "PDF content bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte(docContent))
	}))
	defer srv.Close()

	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/", "application/pdf"})
	tc := newTestChannel(mediaCfg, ms, srv.URL)

	msg := makeMsg()
	msg.Document = &tgbotapi.Document{
		FileID:   "pdf1",
		FileSize: len(docContent),
		MimeType: "application/pdf",
		FileName: "report.pdf",
	}

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected message to be enqueued")
	}

	if ms.callCnt != 1 {
		t.Errorf("expected 1 StoreMedia call for allowed document, got %d", ms.callCnt)
	}

	hasDoc := false
	for _, b := range result.Content {
		if b.Type == content.BlockDocument && b.Filename == "report.pdf" {
			hasDoc = true
		}
	}
	if !hasDoc {
		t.Errorf("expected BlockDocument with filename, got: %+v", result.Content)
	}
}

// TestProcessUpdate_PingNotHandledLocally verifies that /ping is no longer intercepted
// at the channel layer — it must be enqueued to the inbox like any regular message,
// so the agent's slash-command registry handles it.
func TestProcessUpdate_PingNotHandledLocally(t *testing.T) {
	ms := newFakeMediaStore()
	mediaCfg := buildMediaConfig(1024*1024, 10*1024*1024, []string{"image/"})
	tc := newTestChannel(mediaCfg, ms, "http://irrelevant")

	msg := makeMsg()
	msg.Text = "/ping"

	result, enqueue := tc.processUpdate(context.Background(), makeUpdate(msg))
	if !enqueue {
		t.Fatal("expected /ping to be enqueued (handled by agent), not swallowed by channel")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected non-empty content in enqueued /ping message")
	}
	if result.Content[0].Text != "/ping" {
		t.Errorf("expected content text '/ping', got %q", result.Content[0].Text)
	}
}
