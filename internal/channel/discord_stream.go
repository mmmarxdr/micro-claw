package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// BeginStream implements channel.StreamSender for Discord.
// It sends an initial placeholder message ("...") and returns a writer
// that progressively edits that message as chunks arrive.
func (d *DiscordChannel) BeginStream(ctx context.Context, channelID string) (StreamWriter, error) {
	discordChannelID := strings.TrimPrefix(channelID, "discord:")

	// Send placeholder message that we will progressively edit.
	msg, err := d.session.ChannelMessageSend(discordChannelID, "...")
	if err != nil {
		return nil, fmt.Errorf("discord stream: failed to send initial message: %w", err)
	}

	return &discordStreamWriter{
		session:     d.session,
		channelID:   discordChannelID,
		messageID:   msg.ID,
		minInterval: time.Second,
	}, nil
}

// discordStreamWriter implements StreamWriter by accumulating text and
// periodically calling EditMessageText on the initial placeholder message.
type discordStreamWriter struct {
	session     *discordgo.Session
	channelID   string
	messageID   string
	accumulated strings.Builder
	lastFlush   time.Time
	minInterval time.Duration // minimum time between edits (default 1s)
	dirty       bool          // true when accumulated has unflushed content
}

// WriteReasoning is a no-op for Discord — reasoning tokens are not surfaced to the channel.
func (w *discordStreamWriter) WriteReasoning(_ string) error { return nil }

// WriteChunk appends text to the buffer and flushes via message edit
// if enough time has passed since the last edit.
func (w *discordStreamWriter) WriteChunk(text string) error {
	w.accumulated.WriteString(text)
	w.dirty = true

	if time.Since(w.lastFlush) >= w.minInterval {
		return w.flush()
	}
	return nil
}

// Finalize sends the final edit with all accumulated content.
func (w *discordStreamWriter) Finalize() error {
	return w.flush()
}

// Abort appends an error indicator and flushes whatever was accumulated.
func (w *discordStreamWriter) Abort(err error) error {
	w.accumulated.WriteString(fmt.Sprintf("\n\n[Error: %v]", err))
	w.dirty = true
	return w.flush()
}

// flush edits the placeholder message with the current accumulated text.
func (w *discordStreamWriter) flush() error {
	if !w.dirty {
		return nil
	}

	content := w.accumulated.String()
	if content == "" {
		return nil
	}

	// Discord enforces a 2000-character limit per message.
	// Truncate during streaming to stay within bounds; the final full
	// response will be delivered via Send() which already handles chunking.
	const maxChars = 2000
	if len([]rune(content)) > maxChars {
		content = string([]rune(content)[:maxChars])
	}

	edit := &discordgo.MessageEdit{
		Content:   &content,
		Channel:   w.channelID,
		ID:        w.messageID,
	}

	_, err := w.session.ChannelMessageEditComplex(edit)
	if err != nil {
		errStr := err.Error()

		// "Cannot send an empty message" or content unchanged — ignore silently.
		if strings.Contains(errStr, "Cannot send an empty message") ||
			strings.Contains(errStr, "message is not modified") {
			w.dirty = false
			return nil
		}

		// Rate limit (429) — back off, don't fail the stream.
		if strings.Contains(errStr, "429") ||
			strings.Contains(errStr, "rate limit") ||
			strings.Contains(errStr, "Too Many Requests") {
			w.minInterval *= 2
			if w.minInterval > 5*time.Second {
				w.minInterval = 5 * time.Second
			}
			return nil // will retry on next chunk or Finalize
		}

		return err
	}

	w.lastFlush = time.Now()
	w.dirty = false
	return nil
}
