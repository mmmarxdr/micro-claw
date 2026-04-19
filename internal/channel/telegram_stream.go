package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// BeginStream implements channel.StreamSender for Telegram.
// It sends an initial placeholder message ("...") and returns a writer
// that progressively edits that message as chunks arrive.
func (t *TelegramChannel) BeginStream(ctx context.Context, channelID string) (StreamWriter, error) {
	chatStr := strings.TrimPrefix(channelID, "telegram:")
	var chatID int64
	if _, err := fmt.Sscanf(chatStr, "%d", &chatID); err != nil {
		return nil, fmt.Errorf("telegram stream: invalid channel ID %q: %w", channelID, err)
	}

	// Send placeholder message that we will progressively edit.
	msg := tgbotapi.NewMessage(chatID, "...")
	sent, err := t.bot.Send(msg)
	if err != nil {
		return nil, fmt.Errorf("telegram stream: failed to send initial message: %w", err)
	}

	return &telegramStreamWriter{
		bot:         t.bot,
		chatID:      chatID,
		messageID:   sent.MessageID,
		minInterval: time.Second,
	}, nil
}

// telegramStreamWriter implements StreamWriter by accumulating text and
// periodically calling editMessageText on the initial placeholder message.
type telegramStreamWriter struct {
	bot         *tgbotapi.BotAPI
	chatID      int64
	messageID   int
	accumulated strings.Builder
	lastFlush   time.Time
	minInterval time.Duration // minimum time between edits (default 1s)
	dirty       bool          // true when accumulated has unflushed content
}

// WriteChunk appends text to the buffer and flushes via editMessageText
// WriteReasoning is a no-op for Telegram — reasoning tokens are not surfaced to the channel.
func (w *telegramStreamWriter) WriteReasoning(_ string) error { return nil }

// if enough time has passed since the last edit.
func (w *telegramStreamWriter) WriteChunk(text string) error {
	w.accumulated.WriteString(text)
	w.dirty = true

	if time.Since(w.lastFlush) >= w.minInterval {
		return w.flush()
	}
	return nil
}

// Finalize sends the final editMessageText with all accumulated content.
func (w *telegramStreamWriter) Finalize() error {
	return w.flush()
}

// Abort appends an error indicator and flushes whatever was accumulated.
func (w *telegramStreamWriter) Abort(err error) error {
	w.accumulated.WriteString(fmt.Sprintf("\n\n[Error: %v]", err))
	w.dirty = true
	return w.flush()
}

// flush sends the current accumulated text as an editMessageText call.
func (w *telegramStreamWriter) flush() error {
	if !w.dirty {
		return nil
	}

	content := w.accumulated.String()
	if content == "" {
		return nil
	}

	// Telegram enforces a 4096-character limit per message.
	// Truncate to stay within bounds during streaming; the final response
	// will be delivered via Send() which already handles chunking.
	const maxChars = 4096
	if len([]rune(content)) > maxChars {
		content = string([]rune(content)[:maxChars])
	}

	edit := tgbotapi.NewEditMessageText(w.chatID, w.messageID, content)
	_, err := w.bot.Send(edit)
	if err != nil {
		errStr := err.Error()

		// "message is not modified" is expected when content hasn't changed.
		if strings.Contains(errStr, "message is not modified") {
			w.dirty = false
			return nil
		}

		// Rate limit (429) — back off, don't fail the stream.
		if strings.Contains(errStr, "Too Many Requests") || strings.Contains(errStr, "429") {
			w.minInterval = w.minInterval * 2
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
