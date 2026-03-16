package channel

import (
	"context"
	"time"
)

type IncomingMessage struct {
	ID        string
	ChannelID string // e.g., "cli", "telegram:123456"
	SenderID  string
	Text      string
	Metadata  map[string]string // channel-specific data
	Timestamp time.Time
}

type OutgoingMessage struct {
	ChannelID   string
	RecipientID string
	Text        string
	Metadata    map[string]string
}

type Channel interface {
	// Name returns the channel identifier (e.g., "cli", "telegram")
	Name() string

	// Start begins listening for messages and pushes them into inbox.
	// MUST be non-blocking - launch goroutines internally.
	// The channel OWNS its goroutines and must stop them when ctx is cancelled.
	Start(ctx context.Context, inbox chan<- IncomingMessage) error

	// Send delivers a message back through the channel.
	Send(ctx context.Context, msg OutgoingMessage) error

	// Stop gracefully shuts down the channel.
	Stop() error
}
