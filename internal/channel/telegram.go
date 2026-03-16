package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"microagent/internal/config"
)

// TelegramChannel implements the channel.Channel interface using Long Polling.
type TelegramChannel struct {
	bot       *tgbotapi.BotAPI
	config    config.ChannelConfig
	whitelist map[int64]bool
	cancel    context.CancelFunc
}

// NewTelegramChannel initializes the connection and sets up whitelist maps natively.
func NewTelegramChannel(cfg config.ChannelConfig) (*TelegramChannel, error) {
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

	return &TelegramChannel{
		bot:       bot,
		config:    cfg,
		whitelist: whitelist,
	}, nil
}

func (t *TelegramChannel) Name() string {
	return "telegram"
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

				// Only care about actual text messages for MVP
				if update.Message == nil || update.Message.Text == "" {
					continue
				}

				// Verify identity against config statically
				if len(t.whitelist) > 0 && !t.whitelist[update.Message.From.ID] {
					slog.Warn("unauthorized telegram user attempted interaction",
						"user_id", update.Message.From.ID,
						"username", update.Message.From.UserName)
					continue
				}

				slog.Debug("telegram message received",
					"from_id", update.Message.From.ID,
					"username", update.Message.From.UserName,
					"chat_id", update.Message.Chat.ID,
					"text", update.Message.Text,
				)

				// Built-in /ping health check — handled entirely at the channel layer,
				// no LLM call needed. Validates: bot token, polling, whitelist, Send().
				if update.Message.Text == "/ping" {
					reply := tgbotapi.NewMessage(update.Message.Chat.ID, "pong ✅ — micro-claw is alive")
					if _, err := t.bot.Send(reply); err != nil {
						slog.Error("failed to send ping reply", "error", err)
					} else {
						slog.Info("ping replied", "chat_id", update.Message.Chat.ID)
					}
					continue
				}

				// Map the physical identity scope boundary format mathematically matching isolation protocols natively.
				scopeID := fmt.Sprintf("telegram:%d", update.Message.Chat.ID)

				msg := IncomingMessage{
					ID:        fmt.Sprintf("%d", update.Message.MessageID),
					ChannelID: scopeID, // Guaranteed isolated
					SenderID:  fmt.Sprintf("%d", update.Message.From.ID),
					Text:      update.Message.Text,
					Timestamp: time.Unix(int64(update.Message.Date), 0),
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
