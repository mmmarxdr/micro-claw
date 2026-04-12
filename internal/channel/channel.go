package channel

import (
	"context"
	"errors"
	"time"

	"microagent/internal/content"
)

type IncomingMessage struct {
	ID        string
	ChannelID string // e.g., "cli", "telegram:123456"
	SenderID  string
	Content   content.Blocks    // multimodal content blocks
	Metadata  map[string]string // channel-specific data
	Timestamp time.Time
}

// Text returns the text-only representation of the message content.
// Non-text blocks are skipped. This is a convenience method for consumers
// that only need the textual portion of the message.
func (m IncomingMessage) Text() string {
	return m.Content.TextOnly()
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

// StreamWriter writes incremental text to an active stream.
// Created by StreamSender.BeginStream and consumed by the agent loop.
type StreamWriter interface {
	// WriteChunk sends a partial text fragment to the user.
	WriteChunk(text string) error

	// Finalize marks the stream as complete. Called after the last chunk.
	Finalize() error

	// Abort terminates the stream with an error notice.
	Abort(err error) error
}

// StreamSender is an optional interface for channels that support
// progressive/streaming output. Checked via type assertion at runtime;
// channels that don't support streaming simply don't implement it.
type StreamSender interface {
	BeginStream(ctx context.Context, channelID string) (StreamWriter, error)
}

// ErrStreamNotSupported is returned when a channel does not support streaming.
var ErrStreamNotSupported = errors.New("channel does not support streaming")

// TelemetryEmitter is an optional interface for channels that support
// real-time agent telemetry (tool calls, token usage, elapsed time).
// Checked via type assertion; channels that don't support it are silently skipped.
type TelemetryEmitter interface {
	EmitTelemetry(ctx context.Context, channelID string, frame map[string]any) error
}
