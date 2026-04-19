package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
)

// newTestWhatsAppChannel builds a WhatsAppChannel with sensible test defaults.
func newTestWhatsAppChannel(allowedPhones []string) *WhatsAppChannel {
	phones := make(map[string]bool)
	for _, p := range allowedPhones {
		phones[p] = true
	}
	return &WhatsAppChannel{
		phoneNumberID: "TEST_PHONE_ID",
		accessToken:   "TEST_TOKEN",
		verifyToken:   "TEST_VERIFY_TOKEN",
		port:          8080,
		webhookPath:   "/webhook",
		allowedPhones: phones,
		client:        &http.Client{Timeout: 5 * time.Second},
	}
}

// --- Webhook verification ---

func TestWhatsAppChannel_Verification_Valid(t *testing.T) {
	w := newTestWhatsAppChannel(nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook?hub.mode=subscribe&hub.verify_token=TEST_VERIFY_TOKEN&hub.challenge=mychallenge", nil)
	rw := httptest.NewRecorder()

	w.handleVerification(rw, req)

	if rw.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rw.Code)
	}
	if rw.Body.String() != "mychallenge" {
		t.Errorf("expected body 'mychallenge', got %q", rw.Body.String())
	}
}

func TestWhatsAppChannel_Verification_InvalidToken(t *testing.T) {
	w := newTestWhatsAppChannel(nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook?hub.mode=subscribe&hub.verify_token=WRONG&hub.challenge=mychallenge", nil)
	rw := httptest.NewRecorder()

	w.handleVerification(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rw.Code)
	}
}

func TestWhatsAppChannel_Verification_WrongMode(t *testing.T) {
	w := newTestWhatsAppChannel(nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook?hub.mode=unsubscribe&hub.verify_token=TEST_VERIFY_TOKEN&hub.challenge=mychallenge", nil)
	rw := httptest.NewRecorder()

	w.handleVerification(rw, req)

	if rw.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rw.Code)
	}
}

// --- Incoming message parsing ---

func buildWebhookPayload(from, msgType, text string) []byte {
	payload := map[string]any{
		"entry": []map[string]any{
			{
				"changes": []map[string]any{
					{
						"value": map[string]any{
							"metadata": map[string]string{
								"phone_number_id": "TEST_PHONE_ID",
							},
							"messages": []map[string]any{
								{
									"id":   "wamid.test123",
									"from": from,
									"type": msgType,
									"text": map[string]string{"body": text},
								},
							},
						},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func TestWhatsAppChannel_IncomingMessage_TextParsed(t *testing.T) {
	w := newTestWhatsAppChannel(nil)
	inbox := make(chan IncomingMessage, 1)
	ctx := context.Background()

	body := buildWebhookPayload("15551234567", "text", "Hello World")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rw := httptest.NewRecorder()

	w.handleIncoming(rw, req, inbox, ctx)

	if rw.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rw.Code)
	}

	select {
	case msg := <-inbox:
		if msg.ChannelID != "whatsapp:15551234567" {
			t.Errorf("expected channelID 'whatsapp:15551234567', got %q", msg.ChannelID)
		}
		if msg.SenderID != "15551234567" {
			t.Errorf("expected senderID '15551234567', got %q", msg.SenderID)
		}
		if msg.Text() != "Hello World" {
			t.Errorf("expected text 'Hello World', got %q", msg.Text())
		}
		if msg.ID != "wamid.test123" {
			t.Errorf("expected ID 'wamid.test123', got %q", msg.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for message in inbox")
	}
}

// --- Allowlist ---

func TestWhatsAppChannel_Allowlist_AllowedPhonePasses(t *testing.T) {
	w := newTestWhatsAppChannel([]string{"15551234567"})
	inbox := make(chan IncomingMessage, 1)
	ctx := context.Background()

	body := buildWebhookPayload("15551234567", "text", "Hello")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rw := httptest.NewRecorder()

	w.handleIncoming(rw, req, inbox, ctx)

	select {
	case <-inbox:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("allowed phone should have delivered a message")
	}
}

func TestWhatsAppChannel_Allowlist_BlockedPhoneDropped(t *testing.T) {
	w := newTestWhatsAppChannel([]string{"15551234567"})
	inbox := make(chan IncomingMessage, 1)
	ctx := context.Background()

	body := buildWebhookPayload("19999999999", "text", "Unauthorized")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rw := httptest.NewRecorder()

	w.handleIncoming(rw, req, inbox, ctx)

	// Still 200 OK (Meta must always receive 200)
	if rw.Code != http.StatusOK {
		t.Errorf("expected 200 even for blocked phone, got %d", rw.Code)
	}

	select {
	case msg := <-inbox:
		t.Fatalf("blocked phone should not deliver a message, got %+v", msg)
	case <-time.After(50 * time.Millisecond):
		// expected: inbox stays empty
	}
}

// --- Non-text message type: unsupported types ignored, media types handled ---

// TestWhatsAppChannel_UnsupportedTypeIgnored verifies that truly unsupported message
// types (e.g. "sticker") are silently skipped (no inbox enqueue).
func TestWhatsAppChannel_UnsupportedTypeIgnored(t *testing.T) {
	w := newTestWhatsAppChannel(nil)
	inbox := make(chan IncomingMessage, 1)
	ctx := context.Background()

	body := buildWebhookPayload("15551234567", "sticker", "")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rw := httptest.NewRecorder()

	w.handleIncoming(rw, req, inbox, ctx)

	select {
	case msg := <-inbox:
		t.Fatalf("unsupported type should be ignored, got %+v", msg)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestWhatsAppChannel_MediaDisabled_ImageEnqueuesNotice verifies that when media is
// disabled, an image message results in an enqueued notice (not silently dropped).
func TestWhatsAppChannel_MediaDisabled_ImageEnqueuesNotice(t *testing.T) {
	w := newTestWhatsAppChannel(nil)
	// media is disabled (default zero value — mediaStore is nil, Enabled is nil/false)
	inbox := make(chan IncomingMessage, 1)
	ctx := context.Background()

	body := buildWebhookPayload("15551234567", "image", "")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rw := httptest.NewRecorder()

	w.handleIncoming(rw, req, inbox, ctx)

	select {
	case msg := <-inbox:
		if !strings.Contains(msg.Text(), "media ignored") && !strings.Contains(msg.Text(), "media failed") {
			t.Errorf("expected notice about media, got %q", msg.Text())
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected notice message to be enqueued for image with media disabled")
	}
}

// --- Send builds correct HTTP request ---

func TestWhatsAppChannel_Send_BuildsCorrectRequest(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte

	// Mock the Graph API server.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = readAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.sent"}]}`))
	}))
	defer mockServer.Close()

	ch := newTestWhatsAppChannel(nil)
	// Override client and URL by temporarily replacing sendChunk logic via a custom test.
	// We test sendChunk directly by patching the URL through a custom function.
	// Instead, inject a custom client that redirects to mockServer.
	ch.client = mockServer.Client()

	// Build a helper to override the graph API URL — we do this by invoking
	// sendChunk with a replacement URL approach. Since the URL is constructed
	// internally, we test through Send with a real mock that validates the request.

	// Use a custom server URL format: replace the client transport.
	transport := &redirectTransport{baseURL: mockServer.URL}
	ch.client = &http.Client{Transport: transport}

	outMsg := OutgoingMessage{
		ChannelID: "whatsapp:15551234567",
		Text:      "Hello from Microclaw",
	}

	if err := ch.Send(context.Background(), outMsg); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if captured == nil {
		t.Fatal("no HTTP request was captured by the mock server")
	}

	// Validate Authorization header.
	authHeader := captured.Header.Get("Authorization")
	if authHeader != "Bearer TEST_TOKEN" {
		t.Errorf("expected 'Bearer TEST_TOKEN', got %q", authHeader)
	}

	// Validate Content-Type.
	contentType := captured.Header.Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected 'application/json', got %q", contentType)
	}

	// Validate body.
	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if body["messaging_product"] != "whatsapp" {
		t.Errorf("expected messaging_product='whatsapp', got %v", body["messaging_product"])
	}
	if body["to"] != "15551234567" {
		t.Errorf("expected to='15551234567', got %v", body["to"])
	}
	if body["type"] != "text" {
		t.Errorf("expected type='text', got %v", body["type"])
	}
	textMap, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatal("expected 'text' field to be an object")
	}
	if textMap["body"] != "Hello from Microclaw" {
		t.Errorf("expected text.body='Hello from Microclaw', got %v", textMap["body"])
	}
}

// --- Send returns error on non-2xx ---

func TestWhatsAppChannel_Send_NonTwoXxError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid access token"}}`))
	}))
	defer mockServer.Close()

	ch := newTestWhatsAppChannel(nil)
	ch.client = &http.Client{Transport: &redirectTransport{baseURL: mockServer.URL}}

	err := ch.Send(context.Background(), OutgoingMessage{
		ChannelID: "whatsapp:15551234567",
		Text:      "test",
	})

	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to mention 401, got: %v", err)
	}
}

// --- Chunking for long messages ---

func TestWhatsAppChannel_Send_Chunking(t *testing.T) {
	const maxChars = 4000
	callCount := 0

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mockServer.Close()

	ch := newTestWhatsAppChannel(nil)
	ch.client = &http.Client{Transport: &redirectTransport{baseURL: mockServer.URL}}

	longText := strings.Repeat("A", 9000) // 9000 chars → 3 chunks of 4000/4000/1000
	err := ch.Send(context.Background(), OutgoingMessage{
		ChannelID: "whatsapp:15551234567",
		Text:      longText,
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	// Validate chunk math independently (mirrors the chunking logic).
	runes := []rune(longText)
	var chunks []string
	for i := 0; i < len(runes); i += maxChars {
		end := i + maxChars
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(chunks))
	}
	if len([]rune(chunks[0])) != 4000 {
		t.Errorf("expected chunk 1 to be 4000 chars, got %d", len([]rune(chunks[0])))
	}
	if len([]rune(chunks[2])) != 1000 {
		t.Errorf("expected chunk 3 to be 1000 chars, got %d", len([]rune(chunks[2])))
	}

	if callCount != 3 {
		t.Errorf("expected 3 HTTP calls for 3 chunks, got %d", callCount)
	}
}

// --- NewWhatsAppChannel validation ---

func TestNewWhatsAppChannel_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ChannelConfig
	}{
		{
			name: "missing phone_number_id",
			cfg: config.ChannelConfig{
				AccessToken: "tok",
				VerifyToken: "vtok",
			},
		},
		{
			name: "missing access_token",
			cfg: config.ChannelConfig{
				PhoneNumberID: "pid",
				VerifyToken:   "vtok",
			},
		},
		{
			name: "missing verify_token",
			cfg: config.ChannelConfig{
				PhoneNumberID: "pid",
				AccessToken:   "tok",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewWhatsAppChannel(tc.cfg, config.MediaConfig{}, nil)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// --- helpers ---

// readAll reads all bytes from an io.Reader safely.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 512)
	for {
		n, err := r.Read(tmp)
		buf.Write(tmp[:n])
		if err != nil {
			break
		}
	}
	return buf.Bytes(), nil
}

// redirectTransport rewrites requests to target a test server URL,
// preserving path and query.
type redirectTransport struct {
	baseURL string
	inner   http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request so we don't mutate the original.
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = strings.TrimPrefix(rt.baseURL, "http://")

	transport := rt.inner
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(cloned)
}
