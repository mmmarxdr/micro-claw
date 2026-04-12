package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/store"
)

// WhatsAppChannel implements the channel.Channel interface using the
// WhatsApp Cloud API (webhook-based, DMs only).
type WhatsAppChannel struct {
	phoneNumberID string
	accessToken   string
	verifyToken   string
	port          int
	webhookPath   string
	allowedPhones map[string]bool
	httpServer    *http.Server
	client        *http.Client
	media         config.MediaConfig
	mediaStore    store.MediaStore
	// graphClient resolves a media_id to (downloadURL, mimeType, error).
	// Injectable for testing; defaults to real Graph API call.
	graphClient func(ctx context.Context, mediaID string) (downloadURL, mimeType string, err error)
	// mediaClient downloads bytes from a resolved URL with bearer auth.
	// Injectable for testing; defaults to real HTTP GET.
	mediaClient func(ctx context.Context, url string) ([]byte, error)
}

// NewWhatsAppChannel initializes the WhatsApp Cloud API channel.
// mediaStore may be nil — if nil or media.Enabled=false, media messages are ignored.
func NewWhatsAppChannel(cfg config.ChannelConfig, media config.MediaConfig, mediaStore store.MediaStore) (*WhatsAppChannel, error) {
	if cfg.PhoneNumberID == "" {
		return nil, fmt.Errorf("whatsapp: phone_number_id is required")
	}
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("whatsapp: access_token is required")
	}
	if cfg.VerifyToken == "" {
		return nil, fmt.Errorf("whatsapp: verify_token is required")
	}

	// For efficiency, parse allowlist into a constant-time lookup map.
	allowedPhones := make(map[string]bool)
	for _, phone := range cfg.AllowedPhones {
		allowedPhones[phone] = true
	}

	port := cfg.WebhookPort
	if port == 0 {
		port = 8080
	}
	webhookPath := cfg.WebhookPath
	if webhookPath == "" {
		webhookPath = "/webhook"
	}

	w := &WhatsAppChannel{
		phoneNumberID: cfg.PhoneNumberID,
		accessToken:   cfg.AccessToken,
		verifyToken:   cfg.VerifyToken,
		port:          port,
		webhookPath:   webhookPath,
		allowedPhones: allowedPhones,
		client:        &http.Client{Timeout: 30 * time.Second},
		media:         media,
		mediaStore:    mediaStore,
	}

	// Default Graph API resolver: GET https://graph.facebook.com/v20.0/{media_id}
	w.graphClient = w.defaultGraphClient
	// Default media downloader: GET URL with bearer token
	w.mediaClient = w.defaultMediaClient

	return w, nil
}

// isMediaEnabled returns true when media is enabled in config AND a MediaStore is available.
func (w *WhatsAppChannel) isMediaEnabled() bool {
	return config.BoolVal(w.media.Enabled) && w.mediaStore != nil
}

func (w *WhatsAppChannel) Name() string {
	return "whatsapp"
}

// Start registers HTTP handlers and begins listening for webhook events.
// Non-blocking: returns immediately after the server goroutine is launched.
func (w *WhatsAppChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	mux := http.NewServeMux()
	mux.HandleFunc(w.webhookPath, func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.handleVerification(rw, r)
		case http.MethodPost:
			w.handleIncoming(rw, r, inbox, ctx)
		default:
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	addr := fmt.Sprintf(":%d", w.port)
	w.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Bind the listener before returning so that the port is ready when Start returns.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("whatsapp: failed to bind port %d: %w", w.port, err)
	}

	go func() {
		slog.Info("whatsapp webhook server started", "addr", addr, "path", w.webhookPath)
		if serveErr := w.httpServer.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			slog.Error("whatsapp webhook server error", "error", serveErr)
		}
		slog.Info("whatsapp webhook server stopped")
	}()

	// Stop the HTTP server when ctx is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("whatsapp: error during server shutdown", "error", err)
		}
	}()

	return nil
}

// handleVerification responds to the Meta webhook verification GET request.
func (w *WhatsAppChannel) handleVerification(rw http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	if mode == "subscribe" && token == w.verifyToken {
		slog.Info("whatsapp webhook verified successfully")
		rw.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(rw, challenge)
		return
	}

	slog.Warn("whatsapp webhook verification failed", "mode", mode)
	http.Error(rw, "forbidden", http.StatusForbidden)
}

// whatsappMediaRef holds the media ID and optional MIME type from the webhook payload.
type whatsappMediaRef struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Filename string `json:"filename"` // only present for documents
	Caption  string `json:"caption"`  // only present for some types
}

// whatsappPayload is the top-level structure of an inbound webhook POST body.
type whatsappPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					ID        string           `json:"id"`
					From      string           `json:"from"`
					Type      string           `json:"type"`
					Timestamp string           `json:"timestamp"`
					Text      struct{ Body string `json:"body"` } `json:"text"`
					Image     whatsappMediaRef `json:"image"`
					Audio     whatsappMediaRef `json:"audio"`
					Video     whatsappMediaRef `json:"video"`
					Document  whatsappMediaRef `json:"document"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// handleIncoming processes an inbound webhook POST from Meta.
func (w *WhatsAppChannel) handleIncoming(rw http.ResponseWriter, r *http.Request, inbox chan<- IncomingMessage, ctx context.Context) {
	// Meta requires a fast 200 OK; always acknowledge immediately.
	rw.WriteHeader(http.StatusOK)

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB cap
	if err != nil {
		slog.Error("whatsapp: failed to read request body", "error", err)
		return
	}

	var payload whatsappPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("whatsapp: failed to parse webhook payload", "error", err)
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			for _, message := range change.Value.Messages {
				// Check allowlist if configured.
				if len(w.allowedPhones) > 0 && !w.allowedPhones[message.From] {
					slog.Warn("whatsapp: unauthorized sender", "from", message.From)
					continue
				}

				channelID := "whatsapp:" + message.From

				switch message.Type {
				case "text":
					slog.Debug("whatsapp message received",
						"from", message.From,
						"channel_id", channelID,
						"text", message.Text.Body,
					)

					msg := IncomingMessage{
						ID:        message.ID,
						ChannelID: channelID,
						SenderID:  message.From,
						Content:   content.TextBlock(message.Text.Body),
						Timestamp: time.Now(),
					}

					select {
					case inbox <- msg:
					case <-ctx.Done():
						return
					default:
						slog.Warn("whatsapp: inbox full, dropping message", "channel_id", channelID)
					}

				case "image", "audio", "video", "document":
					slog.Debug("whatsapp media message received",
						"from", message.From,
						"type", message.Type,
					)

					var ref whatsappMediaRef
					switch message.Type {
					case "image":
						ref = message.Image
					case "audio":
						ref = message.Audio
					case "video":
						ref = message.Video
					case "document":
						ref = message.Document
					}

					blocks := w.handleMediaMessage(ctx, message.Type, ref)
					msg := IncomingMessage{
						ID:        message.ID,
						ChannelID: channelID,
						SenderID:  message.From,
						Content:   blocks,
						Timestamp: time.Now(),
					}

					select {
					case inbox <- msg:
					case <-ctx.Done():
						return
					default:
						slog.Warn("whatsapp: inbox full, dropping media message", "channel_id", channelID)
					}

				default:
					slog.Debug("whatsapp: ignoring unsupported message type", "type", message.Type, "from", message.From)
				}
			}
		}
	}
}

// handleMediaMessage processes a WhatsApp media message through the two-stage download flow.
// It always returns at least one block (graceful CDN failure path).
func (w *WhatsAppChannel) handleMediaMessage(ctx context.Context, msgType string, ref whatsappMediaRef) content.Blocks {
	if !w.isMediaEnabled() {
		return content.Blocks{{
			Type: content.BlockText,
			Text: "(media ignored — disabled in config)",
		}}
	}

	if ref.ID == "" {
		return content.Blocks{{
			Type: content.BlockText,
			Text: "(media failed to download: missing media ID)",
		}}
	}

	// Pre-flight MIME whitelist check if mime_type provided — saves round trips
	if ref.MimeType != "" {
		allowed := false
		for _, prefix := range w.media.AllowedMIMEPrefixes {
			if strings.HasPrefix(ref.MimeType, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return content.Blocks{{
				Type: content.BlockText,
				Text: fmt.Sprintf("(attachment type not allowed: %s)", ref.MimeType),
			}}
		}
	}

	// Stage 1: resolve media_id → download URL + MIME via Graph API
	downloadURL, resolvedMIME, err := w.graphClient(ctx, ref.ID)
	if err != nil {
		slog.Warn("whatsapp: failed to resolve media URL", "media_id", ref.ID, "error", err)
		return content.Blocks{{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		}}
	}

	// Use the MIME from Graph API response if we didn't have one already
	mime := ref.MimeType
	if mime == "" {
		mime = resolvedMIME
	}

	// Stage 2: download bytes
	data, err := w.mediaClient(ctx, downloadURL)
	if err != nil {
		slog.Warn("whatsapp: failed to download media bytes", "error", err)
		return content.Blocks{{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		}}
	}

	// If MIME still unknown, detect from bytes
	if mime == "" {
		probe := data
		if len(probe) > 512 {
			probe = probe[:512]
		}
		mime = http.DetectContentType(probe)
	}

	// Post-download MIME check (if we didn't have it before)
	if ref.MimeType == "" {
		allowed := false
		for _, prefix := range w.media.AllowedMIMEPrefixes {
			if strings.HasPrefix(mime, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return content.Blocks{{
				Type: content.BlockText,
				Text: fmt.Sprintf("(attachment type not allowed: %s)", mime),
			}}
		}
	}

	// Size gate on downloaded bytes
	if int64(len(data)) > w.media.MaxAttachmentBytes {
		return content.Blocks{{
			Type: content.BlockText,
			Text: fmt.Sprintf("(attachment too large: %d bytes exceeds limit %d)", len(data), w.media.MaxAttachmentBytes),
		}}
	}

	sha, err := w.mediaStore.StoreMedia(ctx, data, mime)
	if err != nil {
		slog.Warn("whatsapp: failed to store media", "error", err)
		return content.Blocks{{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		}}
	}

	var block content.ContentBlock
	switch {
	case strings.HasPrefix(mime, "image/"):
		block = content.ContentBlock{
			Type:        content.BlockImage,
			MediaSHA256: sha,
			MIME:        mime,
			Size:        int64(len(data)),
		}
	case strings.HasPrefix(mime, "audio/"):
		block = content.ContentBlock{
			Type:        content.BlockAudio,
			MediaSHA256: sha,
			MIME:        mime,
			Size:        int64(len(data)),
		}
	default:
		block = content.ContentBlock{
			Type:        content.BlockDocument,
			MediaSHA256: sha,
			MIME:        mime,
			Size:        int64(len(data)),
			Filename:    ref.Filename,
		}
	}

	return content.Blocks{block}
}

// defaultGraphClient calls the Graph API to resolve a media_id to its download URL.
func (w *WhatsAppChannel) defaultGraphClient(ctx context.Context, mediaID string) (downloadURL, mimeType string, err error) {
	url := fmt.Sprintf("https://graph.facebook.com/v20.0/%s", mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("build graph request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.accessToken)

	resp, err := w.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("graph api request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("graph api returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode graph response: %w", err)
	}
	if result.URL == "" {
		return "", "", fmt.Errorf("graph api returned empty download URL")
	}

	return result.URL, result.MimeType, nil
}

// defaultMediaClient downloads media bytes from a signed URL with bearer auth.
func (w *WhatsAppChannel) defaultMediaClient(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build media request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.accessToken)

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("media CDN returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read media body: %w", err)
	}

	return data, nil
}

// Send delivers a text reply to a WhatsApp phone number via the Cloud API.
// Long messages are chunked at 4000 characters to stay within limits.
func (w *WhatsAppChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	// Strip "whatsapp:" prefix to obtain the phone number.
	phone := strings.TrimPrefix(msg.ChannelID, "whatsapp:")

	const maxChars = 4000
	runes := []rune(msg.Text)
	length := len(runes)

	for i := 0; i < length; i += maxChars {
		end := i + maxChars
		if end > length {
			end = length
		}

		chunk := string(runes[i:end])
		if chunk == "" {
			continue
		}

		if err := w.sendChunk(ctx, phone, chunk); err != nil {
			return err
		}
	}

	return nil
}

// sendChunk posts a single text chunk to the WhatsApp Cloud API.
func (w *WhatsAppChannel) sendChunk(ctx context.Context, phone, text string) error {
	url := fmt.Sprintf("https://graph.facebook.com/v20.0/%s/messages", w.phoneNumberID)

	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                phone,
		"type":              "text",
		"text": map[string]string{
			"body": text,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("whatsapp: failed to marshal send payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("whatsapp: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.accessToken)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: send request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("whatsapp: send returned non-2xx status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (w *WhatsAppChannel) Stop() error {
	if w.httpServer == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.httpServer.Shutdown(shutdownCtx)
}
