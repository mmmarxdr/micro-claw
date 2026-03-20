package channel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// MultiplexChannel fans multiple Channel implementations into a single inbox
// and routes Send calls by ChannelID prefix.
type MultiplexChannel struct {
	children []Channel
	mu       sync.RWMutex
}

// NewMultiplexChannel creates a mux over the given channels.
// Panics if channels is empty.
func NewMultiplexChannel(channels []Channel) *MultiplexChannel {
	if len(channels) == 0 {
		panic("MultiplexChannel: at least one channel required")
	}
	return &MultiplexChannel{children: channels}
}

// Name returns "mux".
func (m *MultiplexChannel) Name() string { return "mux" }

// Start calls Start on every child (each gets its own buffered childInbox) and
// fans all child inboxes into the shared inbox. Non-blocking: returns nil immediately.
// If a child fails to Start, the error is logged and that child is skipped.
func (m *MultiplexChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	m.mu.RLock()
	children := make([]Channel, len(m.children))
	copy(children, m.children)
	m.mu.RUnlock()

	for _, child := range children {
		childInbox := make(chan IncomingMessage, 64)

		if err := child.Start(ctx, childInbox); err != nil {
			slog.Error("mux: child channel failed to start", "name", child.Name(), "error", err)
			continue
		}

		// Fan-in: forward messages from this child's inbox to the shared inbox.
		go func(ch Channel, src <-chan IncomingMessage) {
			for {
				select {
				case msg, ok := <-src:
					if !ok {
						return
					}
					select {
					case inbox <- msg:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(child, childInbox)
	}

	return nil
}

// Send routes msg to the child whose Name() matches the ChannelID prefix.
// Matches "child-name:<anything>" or exact "child-name".
// Returns an error if no child matches.
func (m *MultiplexChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, child := range m.children {
		name := child.Name()
		if strings.HasPrefix(msg.ChannelID, name+":") || msg.ChannelID == name {
			return child.Send(ctx, msg)
		}
	}

	return fmt.Errorf("mux: no channel found for channelID: %s", msg.ChannelID)
}

// Stop calls Stop on every child and returns the first non-nil error encountered.
func (m *MultiplexChannel) Stop() error {
	m.mu.RLock()
	children := make([]Channel, len(m.children))
	copy(children, m.children)
	m.mu.RUnlock()

	var firstErr error
	for _, child := range children {
		if err := child.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
