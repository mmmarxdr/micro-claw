package channel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/store"

	"github.com/google/uuid"
)

type CLIChannel struct {
	config     config.ChannelConfig
	media      config.MediaConfig
	mediaStore store.MediaStore
	in         io.Reader
	out        io.Writer
}

// NewCLIChannel creates a CLIChannel with injectable I/O for testing.
// mediaStore may be nil — if nil or media.Enabled=false, /attach is disabled.
func NewCLIChannel(cfg config.ChannelConfig, media config.MediaConfig, mediaStore store.MediaStore, in io.Reader, out io.Writer) *CLIChannel {
	return &CLIChannel{config: cfg, media: media, mediaStore: mediaStore, in: in, out: out}
}

// NewCLIChannelDefault creates a CLIChannel using os.Stdin and os.Stdout.
func NewCLIChannelDefault(cfg config.ChannelConfig, media config.MediaConfig, mediaStore store.MediaStore) *CLIChannel {
	return NewCLIChannel(cfg, media, mediaStore, os.Stdin, os.Stdout)
}

func (c *CLIChannel) Name() string { return "cli" }

// isMediaEnabled returns true when media is enabled in config AND a MediaStore is available.
func (c *CLIChannel) isMediaEnabled() bool {
	return config.BoolVal(c.media.Enabled) && c.mediaStore != nil
}

func (c *CLIChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	go func() {
		scanner := bufio.NewScanner(c.in)
		fmt.Fprintln(c.out, "MicroAgent CLI started. Type your message and press ENTER (Ctrl+C to exit):")
		fmt.Fprint(c.out, "> ")

		lineCh := make(chan string)
		errCh := make(chan error, 1)

		go func() {
			for scanner.Scan() {
				lineCh <- scanner.Text()
			}
			errCh <- scanner.Err()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errCh:
				if err != nil {
					fmt.Fprintf(c.out, "Error reading stdin: %v\n", err)
				}
				return
			case text := <-lineCh:
				if text == "" {
					continue
				}
				// Check for /attach <path> command
				if strings.HasPrefix(text, "/attach ") {
					path := strings.TrimPrefix(text, "/attach ")
					path = strings.TrimSpace(path)
					msg, ok := c.handleAttach(ctx, path)
					if ok {
						select {
						case inbox <- msg:
						case <-ctx.Done():
							return
						}
					}
					fmt.Fprint(c.out, "> ")
					continue
				}
				inbox <- IncomingMessage{
					ID:        uuid.New().String(),
					ChannelID: "cli",
					SenderID:  "local_user",
					Content:   content.TextBlock(text),
					Timestamp: time.Now(),
				}
			}
		}
	}()
	return nil
}

// handleAttach reads a file from path and builds a media IncomingMessage.
// Returns false (with no message) when the attachment should be rejected.
func (c *CLIChannel) handleAttach(ctx context.Context, path string) (IncomingMessage, bool) {
	if !c.isMediaEnabled() {
		fmt.Fprintln(c.out, "media disabled — /attach ignored")
		return IncomingMessage{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(c.out, "error reading file: %v\n", err)
		return IncomingMessage{}, false
	}

	// Detect MIME from first 512 bytes (or full content if smaller)
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	mime := http.DetectContentType(probe)

	// Size gate
	if int64(len(data)) > c.media.MaxAttachmentBytes {
		fmt.Fprintf(c.out, "attachment too large: %d bytes exceeds limit %d\n", len(data), c.media.MaxAttachmentBytes)
		return IncomingMessage{}, false
	}

	// MIME whitelist check
	allowed := false
	for _, prefix := range c.media.AllowedMIMEPrefixes {
		if strings.HasPrefix(mime, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		fmt.Fprintf(c.out, "attachment type not allowed: %s\n", mime)
		return IncomingMessage{}, false
	}

	sha, err := c.mediaStore.StoreMedia(ctx, data, mime)
	if err != nil {
		slog.Warn("cli: failed to store media", "error", err)
		fmt.Fprintf(c.out, "failed to store attachment: %v\n", err)
		return IncomingMessage{}, false
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
			Filename:    filepath.Base(path),
		}
	}

	return IncomingMessage{
		ID:        uuid.New().String(),
		ChannelID: "cli",
		SenderID:  "local_user",
		Content:   content.Blocks{block},
		Timestamp: time.Now(),
	}, true
}

func (c *CLIChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	fmt.Fprintf(c.out, "\nAgent: %s\n> ", msg.Text)
	return nil
}

func (c *CLIChannel) Stop() error {
	return nil
}
