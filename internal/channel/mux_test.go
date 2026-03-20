package channel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// mockChannel — minimal Channel implementation for mux tests.
// ---------------------------------------------------------------------------

type muxMockChannel struct {
	name    string
	msgs    []IncomingMessage // pre-filled; emitted on Start
	sent    []OutgoingMessage
	stopErr error
	started bool
}

func (m *muxMockChannel) Name() string { return m.name }

func (m *muxMockChannel) Start(ctx context.Context, inbox chan<- IncomingMessage) error {
	m.started = true
	for _, msg := range m.msgs {
		inbox <- msg
	}
	return nil
}

func (m *muxMockChannel) Send(ctx context.Context, msg OutgoingMessage) error {
	m.sent = append(m.sent, msg)
	return nil
}

func (m *muxMockChannel) Stop() error { return m.stopErr }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMux_Start_FansInbox(t *testing.T) {
	ch1 := &muxMockChannel{
		name: "ch1",
		msgs: []IncomingMessage{{ChannelID: "ch1", Text: "hello from ch1"}},
	}
	ch2 := &muxMockChannel{
		name: "ch2",
		msgs: []IncomingMessage{{ChannelID: "ch2", Text: "hello from ch2"}},
	}

	mux := NewMultiplexChannel([]Channel{ch1, ch2})
	inbox := make(chan IncomingMessage, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := mux.Start(ctx, inbox); err != nil {
		t.Fatalf("Start returned unexpected error: %v", err)
	}

	received := make(map[string]bool)
	deadline := time.After(1 * time.Second)
	for len(received) < 2 {
		select {
		case msg := <-inbox:
			received[msg.ChannelID] = true
		case <-deadline:
			t.Fatalf("timed out waiting for messages; received: %v", received)
		}
	}

	if !received["ch1"] {
		t.Error("expected message from ch1")
	}
	if !received["ch2"] {
		t.Error("expected message from ch2")
	}
}

func TestMux_Send_RoutesByPrefix(t *testing.T) {
	cronCh := &muxMockChannel{name: "cron"}
	cliCh := &muxMockChannel{name: "cli"}

	mux := NewMultiplexChannel([]Channel{cronCh, cliCh})

	ctx := context.Background()
	msg := OutgoingMessage{ChannelID: "cron:abc123", Text: "result"}

	if err := mux.Send(ctx, msg); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	if len(cronCh.sent) != 1 {
		t.Errorf("expected 1 message sent to cron channel, got %d", len(cronCh.sent))
	}
	if cronCh.sent[0].ChannelID != "cron:abc123" {
		t.Errorf("unexpected channelID: %s", cronCh.sent[0].ChannelID)
	}
	if len(cliCh.sent) != 0 {
		t.Errorf("cli channel should not have received any messages")
	}
}

func TestMux_Send_ExactNameMatch(t *testing.T) {
	cliCh := &muxMockChannel{name: "cli"}
	mux := NewMultiplexChannel([]Channel{cliCh})

	ctx := context.Background()
	msg := OutgoingMessage{ChannelID: "cli", Text: "direct"}

	if err := mux.Send(ctx, msg); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	if len(cliCh.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(cliCh.sent))
	}
}

func TestMux_Send_NoMatch(t *testing.T) {
	cliCh := &muxMockChannel{name: "cli"}
	mux := NewMultiplexChannel([]Channel{cliCh})

	ctx := context.Background()
	msg := OutgoingMessage{ChannelID: "telegram:999", Text: "oops"}

	err := mux.Send(ctx, msg)
	if err == nil {
		t.Fatal("expected error for unknown channelID, got nil")
	}
	if !containsStr(err.Error(), "no channel found") {
		t.Errorf("expected 'no channel found' in error, got: %v", err)
	}
}

func TestMux_Stop_NoError(t *testing.T) {
	ch1 := &muxMockChannel{name: "ch1"}
	ch2 := &muxMockChannel{name: "ch2"}
	mux := NewMultiplexChannel([]Channel{ch1, ch2})

	inbox := make(chan IncomingMessage, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = mux.Start(ctx, inbox)

	if err := mux.Stop(); err != nil {
		t.Errorf("expected nil error from Stop, got: %v", err)
	}
}

func TestMux_Stop_ReturnsFirstError(t *testing.T) {
	stopErr := errors.New("stop failed")
	ch1 := &muxMockChannel{name: "ch1", stopErr: stopErr}
	ch2 := &muxMockChannel{name: "ch2"}
	mux := NewMultiplexChannel([]Channel{ch1, ch2})

	err := mux.Stop()
	if !errors.Is(err, stopErr) {
		t.Errorf("expected stopErr, got: %v", err)
	}
}

func TestMux_Panic_EmptyChildren(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty children, got none")
		}
	}()
	NewMultiplexChannel(nil)
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
