package channel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/store"
)

// TelegramChannel implements the channel.Channel interface using Long Polling.
type TelegramChannel struct {
	bot          *tgbotapi.BotAPI
	config       config.ChannelConfig
	media        config.MediaConfig
	mediaStore   store.MediaStore
	whitelist    map[int64]bool
	cancel       context.CancelFunc
	getDirectURL func(fileID string) (string, error)
	httpClient   *http.Client
}

// NewTelegramChannel initializes the connection and sets up whitelist maps natively.
// mediaStore may be nil — if nil or media.Enabled=false, media is treated as disabled.
func NewTelegramChannel(cfg config.ChannelConfig, media config.MediaConfig, mediaStore store.MediaStore) (*TelegramChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram token is required")
	}

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize telegram bot: %w", err)
	}

	// For efficiency, parse whitelist array into a constant-time lookup map.
	whitelist := make(map[int64]bool)
	for _, id := range cfg.AllowedUsers {
		whitelist[id] = true
	}

	// Disable debug mode by default, rely on structured logging from standard setup
	bot.Debug = false

	tc := &TelegramChannel{
		bot:        bot,
		config:     cfg,
		media:      media,
		mediaStore: mediaStore,
		whitelist:  whitelist,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	tc.getDirectURL = bot.GetFileDirectURL
	return tc, nil
}

func (t *TelegramChannel) Name() string {
	return "telegram"
}

// isMediaEnabled returns true when media is enabled in config AND a MediaStore is available.
func (t *TelegramChannel) isMediaEnabled() bool {
	return config.BoolVal(t.media.Enabled) && t.mediaStore != nil
}

// hasMedia returns true if the message contains a photo, voice note, or document.
func hasMedia(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	return len(msg.Photo) > 0 || msg.Voice != nil || msg.Document != nil
}

// processUpdate handles a single Telegram update and returns the IncomingMessage to enqueue
// plus a boolean indicating whether it should be enqueued. Returns false when the update
// should be silently skipped (whitelist rejection, nil message, etc.).
func (t *TelegramChannel) processUpdate(ctx context.Context, update tgbotapi.Update) (IncomingMessage, bool) {
	if update.Message == nil {
		return IncomingMessage{}, false
	}

	msg := update.Message

	// Only process text messages or messages with media/caption — skip pure service messages
	hasText := msg.Text != ""
	hasCaption := msg.Caption != ""
	hasMed := hasMedia(msg)
	if !hasText && !hasCaption && !hasMed {
		return IncomingMessage{}, false
	}

	// Verify identity against config statically
	if len(t.whitelist) > 0 && !t.whitelist[msg.From.ID] {
		slog.Warn("unauthorized telegram user attempted interaction",
			"user_id", msg.From.ID,
			"username", msg.From.UserName)
		return IncomingMessage{}, false
	}

	slog.Debug("telegram message received",
		"from_id", msg.From.ID,
		"username", msg.From.UserName,
		"chat_id", msg.Chat.ID,
		"text", msg.Text,
	)

	scopeID := fmt.Sprintf("telegram:%d", msg.Chat.ID)
	ts := time.Unix(int64(msg.Date), 0)

	var blocks content.Blocks

	if !hasMed {
		// Pure text message — fast path
		blocks = content.TextBlock(msg.Text)
	} else {
		// Media present — route through media handling
		blocks = t.handleMedia(ctx, msg)
	}

	return IncomingMessage{
		ID:        fmt.Sprintf("%d", msg.MessageID),
		ChannelID: scopeID,
		SenderID:  fmt.Sprintf("%d", msg.From.ID),
		Content:   blocks,
		Timestamp: ts,
	}, true
}

// handleMedia processes a message that contains at least one media attachment.
// It enforces enabled guard, size gates, and dispatches to type-specific handlers.
func (t *TelegramChannel) handleMedia(ctx context.Context, msg *tgbotapi.Message) content.Blocks {
	caption := msg.Caption

	// 6.1 — media.enabled=false guard
	if !t.isMediaEnabled() {
		var blocks content.Blocks
		if caption != "" {
			blocks = append(blocks, content.ContentBlock{Type: content.BlockText, Text: caption})
		}
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: "(media ignored — disabled in config)",
		})
		return blocks
	}

	// Dispatch by media type (Telegram only sends one at a time)
	switch {
	case len(msg.Photo) > 0:
		return t.handlePhoto(ctx, msg)
	case msg.Voice != nil:
		return t.handleVoice(ctx, msg)
	case msg.Document != nil:
		return t.handleDocument(ctx, msg)
	}

	// Should never reach here, but fall back to caption text
	if caption != "" {
		return content.TextBlock(caption)
	}
	return content.Blocks{}
}

// captionBlock returns a Blocks slice containing just the caption text block,
// or nil if the caption is empty.
func captionBlocks(caption string) content.Blocks {
	if caption == "" {
		return nil
	}
	return content.Blocks{{Type: content.BlockText, Text: caption}}
}

// handlePhoto downloads and stores a photo attachment.
func (t *TelegramChannel) handlePhoto(ctx context.Context, msg *tgbotapi.Message) content.Blocks {
	caption := msg.Caption
	// Use highest-resolution photo (last element)
	photo := msg.Photo[len(msg.Photo)-1]

	// 6.2 — pre-download size gate
	fileSize := int64(photo.FileSize)
	if fileSize > t.media.MaxAttachmentBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(attachment too large: %d bytes exceeds limit %d)", fileSize, t.media.MaxAttachmentBytes),
		})
		return blocks
	}

	// Total message bytes gate (caption + attachment)
	totalBytes := fileSize + int64(len(caption))
	if totalBytes > t.media.MaxMessageBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(message too large: %d bytes exceeds total limit %d)", totalBytes, t.media.MaxMessageBytes),
		})
		return blocks
	}

	// 6.3 — photo download
	data, mime, err := t.downloadMedia(ctx, photo.FileID)
	if err != nil {
		slog.Warn("telegram photo download failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	sha, err := t.mediaStore.StoreMedia(ctx, data, mime)
	if err != nil {
		slog.Warn("telegram photo store failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	blocks := captionBlocks(caption)
	blocks = append(blocks, content.ContentBlock{
		Type:        content.BlockImage,
		MediaSHA256: sha,
		MIME:        mime,
		Size:        int64(len(data)),
	})
	return blocks
}

// handleVoice downloads and stores a voice note attachment.
func (t *TelegramChannel) handleVoice(ctx context.Context, msg *tgbotapi.Message) content.Blocks {
	caption := msg.Caption
	voice := msg.Voice

	// 6.2 — pre-download size gate
	fileSize := int64(voice.FileSize)
	if fileSize > t.media.MaxAttachmentBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(attachment too large: %d bytes exceeds limit %d)", fileSize, t.media.MaxAttachmentBytes),
		})
		return blocks
	}

	totalBytes := fileSize + int64(len(caption))
	if totalBytes > t.media.MaxMessageBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(message too large: %d bytes exceeds total limit %d)", totalBytes, t.media.MaxMessageBytes),
		})
		return blocks
	}

	// 6.4 — voice download; Telegram voice notes are always OGG
	const voiceMIME = "audio/ogg"
	data, _, err := t.downloadMedia(ctx, voice.FileID)
	if err != nil {
		slog.Warn("telegram voice download failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	sha, err := t.mediaStore.StoreMedia(ctx, data, voiceMIME)
	if err != nil {
		slog.Warn("telegram voice store failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	blocks := captionBlocks(caption)
	blocks = append(blocks, content.ContentBlock{
		Type:        content.BlockAudio,
		MediaSHA256: sha,
		MIME:        voiceMIME,
		Size:        int64(len(data)),
	})
	return blocks
}

// handleDocument downloads and stores a document attachment after MIME whitelist check.
func (t *TelegramChannel) handleDocument(ctx context.Context, msg *tgbotapi.Message) content.Blocks {
	caption := msg.Caption
	doc := msg.Document

	// 6.5 — MIME whitelist check BEFORE download
	if doc.MimeType == "" {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: "(attachment type not allowed: unknown MIME type)",
		})
		return blocks
	}

	allowed := false
	for _, prefix := range t.media.AllowedMIMEPrefixes {
		if strings.HasPrefix(doc.MimeType, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(attachment type not allowed: %s)", doc.MimeType),
		})
		return blocks
	}

	// 6.2 — pre-download size gate
	fileSize := int64(doc.FileSize)
	if fileSize > t.media.MaxAttachmentBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(attachment too large: %d bytes exceeds limit %d)", fileSize, t.media.MaxAttachmentBytes),
		})
		return blocks
	}

	totalBytes := fileSize + int64(len(caption))
	if totalBytes > t.media.MaxMessageBytes {
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(message too large: %d bytes exceeds total limit %d)", totalBytes, t.media.MaxMessageBytes),
		})
		return blocks
	}

	data, _, err := t.downloadMedia(ctx, doc.FileID)
	if err != nil {
		slog.Warn("telegram document download failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	sha, err := t.mediaStore.StoreMedia(ctx, data, doc.MimeType)
	if err != nil {
		slog.Warn("telegram document store failed", "error", err)
		blocks := captionBlocks(caption)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
		})
		return blocks
	}

	blocks := captionBlocks(caption)
	blocks = append(blocks, content.ContentBlock{
		Type:        content.BlockDocument,
		MediaSHA256: sha,
		MIME:        doc.MimeType,
		Size:        int64(len(data)),
		Filename:    doc.FileName,
	})
	return blocks
}

// downloadMedia fetches raw bytes from the Telegram CDN for a given fileID.
// It uses GetFileDirectURL to get the CDN URL, then performs an HTTP GET.
// MIME type is detected from the first 512 bytes after download.
func (t *TelegramChannel) downloadMedia(ctx context.Context, fileID string) ([]byte, string, error) {
	fileURL, err := t.getDirectURL(fileID)
	if err != nil {
		return nil, "", fmt.Errorf("get file URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("CDN returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}

	// Detect MIME after download — more reliable than trusting Telegram metadata
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	mime := http.DetectContentType(probe)

	return data, mime, nil
}

// Start spawns a background Goroutine that fetches updates and translates payloads
// directly to the isolated micro-agent inbox loops dynamically resolving Chat IDs.
func (t *TelegramChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	// Telegram specific update pulling parameters
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60 // Long polling duration bounds

	updates := t.bot.GetUpdatesChan(u)

	// Create a sub-context for local cancellation wrapper
	stopCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	go func() {
		slog.Info("telegram polling started", "bot_username", t.bot.Self.UserName)
		for {
			select {
			case <-stopCtx.Done():
				t.bot.StopReceivingUpdates()
				slog.Info("telegram polling stopped gracefully")
				return
			case update, ok := <-updates:
				if !ok {
					slog.Warn("telegram updates channel abruptly closed, recreating pipeline")
					updates = t.bot.GetUpdatesChan(u)
					continue
				}

				msg, enqueue := t.processUpdate(stopCtx, update)
				if !enqueue {
					continue
				}

				// Drop in the inbox without blocking the physical receiver.
				select {
				case inbox <- msg:
				case <-stopCtx.Done():
					return
				default:
					slog.Warn("inbox is currently full, dropping telegram message", "channel_id", msg.ChannelID)
				}
			}
		}
	}()

	return nil
}

func (t *TelegramChannel) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

// Send accepts outputs from the Language Model and passes them cleanly back resolving explicit boundaries securely.
// Uses a string chunking parser to obey the 4096-character API envelope natively.
func (t *TelegramChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	// Reversibly strip "telegram:" prefix to obtain physical CHAT_ID
	chatStr := strings.TrimPrefix(msg.ChannelID, "telegram:")
	var chatID int64
	_, err := fmt.Sscanf(chatStr, "%d", &chatID)
	if err != nil {
		return fmt.Errorf("invalid telegram channel ID routing format %q: %w", msg.ChannelID, err)
	}

	const maxChars = 4000 // Buffer below 4096 just to be absolutely safe
	runes := []rune(msg.Text)
	length := len(runes)

	for i := 0; i < length; i += maxChars {
		end := i + maxChars
		if end > length {
			end = length
		}

		chunk := string(runes[i:end])
		if chunk == "" {
			continue // Skip empty LLM drops natively
		}

		tgMsg := tgbotapi.NewMessage(chatID, chunk)
		_, err := t.bot.Send(tgMsg)
		if err != nil {
			return fmt.Errorf("failed to send telegram chunk payload: %w", err)
		}
	}

	return nil
}
