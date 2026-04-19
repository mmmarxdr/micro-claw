package channel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/store"
)

// DiscordChannel implements the channel.Channel interface using Discord's Gateway (WebSocket).
type DiscordChannel struct {
	session         *discordgo.Session
	allowedGuilds   map[string]bool
	allowedChannels map[string]bool
	cancel          context.CancelFunc
	media           config.MediaConfig
	mediaStore      store.MediaStore
	httpClient      *http.Client
}

// NewDiscordChannel initializes the Discord session and sets up guild/channel allowlists.
// mediaStore may be nil — if nil or media.Enabled=false, attachments are ignored.
func NewDiscordChannel(cfg config.ChannelConfig, media config.MediaConfig, mediaStore store.MediaStore) (*DiscordChannel, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("discord token is required")
	}

	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize discord session: %w", err)
	}

	// Require message content intent to receive message bodies.
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	// For efficiency, parse allowlist slices into constant-time lookup maps.
	allowedGuilds := make(map[string]bool)
	for _, id := range cfg.AllowedGuilds {
		allowedGuilds[id] = true
	}

	allowedChannels := make(map[string]bool)
	for _, id := range cfg.AllowedChannels {
		allowedChannels[id] = true
	}

	return &DiscordChannel{
		session:         session,
		allowedGuilds:   allowedGuilds,
		allowedChannels: allowedChannels,
		media:           media,
		mediaStore:      mediaStore,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (d *DiscordChannel) Name() string {
	return "discord"
}

// isMediaEnabled returns true when media is enabled in config AND a MediaStore is available.
func (d *DiscordChannel) isMediaEnabled() bool {
	return config.BoolVal(d.media.Enabled) && d.mediaStore != nil
}

// processDiscordMessage converts a discordgo.MessageCreate into an IncomingMessage.
// Returns the message and true if it should be enqueued, or zero value and false to skip.
func (d *DiscordChannel) processDiscordMessage(ctx context.Context, m *discordgo.MessageCreate) (IncomingMessage, bool) {
	// Attribute the sender so the LLM knows who is speaking in a team channel.
	text := fmt.Sprintf("[%s]: %s", m.Author.Username, m.Content)

	var blocks content.Blocks

	if len(m.Attachments) == 0 {
		// No attachments — fast path
		blocks = content.TextBlock(text)
	} else if !d.isMediaEnabled() {
		// Media disabled but attachments present — enqueue with notice
		blocks = content.TextBlock(text)
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: "(media ignored — disabled in config)",
		})
	} else {
		// Process attachments
		blocks = d.handleAttachments(ctx, m.Content, m.Author.Username, m.Attachments)
	}

	return IncomingMessage{
		ID:        m.ID,
		ChannelID: "discord:" + m.ChannelID,
		SenderID:  m.Author.ID,
		Content:   blocks,
		Timestamp: time.Now(),
	}, true
}

// handleAttachments processes Discord message attachments and builds content blocks.
func (d *DiscordChannel) handleAttachments(ctx context.Context, msgContent, username string, attachments []*discordgo.MessageAttachment) content.Blocks {
	// Attribution prefix block
	prefix := fmt.Sprintf("[%s]: %s", username, msgContent)
	var blocks content.Blocks
	if prefix != "" {
		blocks = append(blocks, content.ContentBlock{Type: content.BlockText, Text: prefix})
	}

	// Total message bytes gate: sum of all attachment sizes + text content
	var totalBytes int64
	totalBytes += int64(len(msgContent))
	for _, att := range attachments {
		totalBytes += int64(att.Size)
	}
	if totalBytes > d.media.MaxMessageBytes {
		blocks = append(blocks, content.ContentBlock{
			Type: content.BlockText,
			Text: fmt.Sprintf("(message too large: %d bytes exceeds total limit %d)", totalBytes, d.media.MaxMessageBytes),
		})
		return blocks
	}

	for _, att := range attachments {
		// Per-attachment size gate
		if int64(att.Size) > d.media.MaxAttachmentBytes {
			blocks = append(blocks, content.ContentBlock{
				Type: content.BlockText,
				Text: fmt.Sprintf("(attachment too large: %d bytes exceeds limit %d)", att.Size, d.media.MaxAttachmentBytes),
			})
			continue
		}

		// Download attachment
		data, mime, err := d.downloadURL(ctx, att.URL, att.ContentType)
		if err != nil {
			slog.Warn("discord: attachment download failed", "url", att.URL, "error", err)
			blocks = append(blocks, content.ContentBlock{
				Type: content.BlockText,
				Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
			})
			continue
		}

		// MIME whitelist check
		allowed := false
		for _, prefix := range d.media.AllowedMIMEPrefixes {
			if strings.HasPrefix(mime, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			blocks = append(blocks, content.ContentBlock{
				Type: content.BlockText,
				Text: fmt.Sprintf("(attachment type not allowed: %s)", mime),
			})
			continue
		}

		sha, err := d.mediaStore.StoreMedia(ctx, data, mime)
		if err != nil {
			slog.Warn("discord: failed to store attachment", "error", err)
			blocks = append(blocks, content.ContentBlock{
				Type: content.BlockText,
				Text: fmt.Sprintf("(media failed to download: %s)", err.Error()),
			})
			continue
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
				Filename:    att.Filename,
			}
		}
		blocks = append(blocks, block)
	}

	return blocks
}

// downloadURL fetches bytes from a URL. If contentTypeHint is non-empty it is used as MIME;
// otherwise MIME is detected from the response body.
func (d *DiscordChannel) downloadURL(ctx context.Context, url, contentTypeHint string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}

	resp, err := d.httpClient.Do(req)
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

	mime := contentTypeHint
	if mime == "" {
		probe := data
		if len(probe) > 512 {
			probe = probe[:512]
		}
		mime = http.DetectContentType(probe)
	}

	return data, mime, nil
}

// Start registers the MessageCreate handler, opens the WebSocket connection,
// and launches a goroutine to watch for context cancellation.
// MUST be non-blocking — the handler runs asynchronously via discordgo's dispatch loop.
func (d *DiscordChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	stopCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel

	d.session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Ignore messages from bots (including ourselves).
		if m.Author == nil || m.Author.Bot {
			return
		}

		// Empty content with no attachments is not actionable.
		if strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 {
			return
		}

		// Enforce guild allowlist when configured.
		if len(d.allowedGuilds) > 0 && !d.allowedGuilds[m.GuildID] {
			slog.Warn("discord message from unauthorized guild",
				"guild_id", m.GuildID,
				"author", m.Author.Username,
			)
			return
		}

		// Enforce channel allowlist when configured.
		if len(d.allowedChannels) > 0 && !d.allowedChannels[m.ChannelID] {
			slog.Warn("discord message from unauthorized channel",
				"channel_id", m.ChannelID,
				"author", m.Author.Username,
			)
			return
		}

		slog.Debug("discord message received",
			"channel_id", m.ChannelID,
			"guild_id", m.GuildID,
			"author", m.Author.Username,
			"content", m.Content,
			"attachments", len(m.Attachments),
		)

		msg, enqueue := d.processDiscordMessage(stopCtx, m)
		if !enqueue {
			return
		}

		// Non-blocking push: drop if inbox is full rather than blocking the dispatch loop.
		select {
		case inbox <- msg:
		case <-stopCtx.Done():
			return
		default:
			slog.Warn("inbox is currently full, dropping discord message", "channel_id", msg.ChannelID)
		}
	})

	if err := d.session.Open(); err != nil {
		cancel()
		return fmt.Errorf("failed to open discord session: %w", err)
	}

	slog.Info("discord gateway connected")

	// Watch for context cancellation and stop the session cleanly.
	go func() {
		<-stopCtx.Done()
		if err := d.session.Close(); err != nil {
			slog.Warn("discord session close error", "error", err)
		}
		slog.Info("discord gateway disconnected")
	}()

	return nil
}

// Stop cancels the internal context, which triggers the watcher goroutine to
// close the Discord session.
func (d *DiscordChannel) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	return nil
}

// Send delivers a message to a Discord channel, chunking at 1900 characters
// to stay safely under Discord's 2000-character limit.
func (d *DiscordChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	channelID := strings.TrimPrefix(msg.ChannelID, "discord:")

	const maxChars = 1900
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

		if _, err := d.session.ChannelMessageSend(channelID, chunk); err != nil {
			return fmt.Errorf("failed to send discord message chunk: %w", err)
		}
	}

	return nil
}
