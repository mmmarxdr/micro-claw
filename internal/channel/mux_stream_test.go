package channel

import (
	"context"
	"errors"
	"testing"
)

// --------------------------------------------------------------------------
// mockStreamChannel — sub-channel that implements StreamSender
// --------------------------------------------------------------------------

type mockStreamWriter struct {
	chunks    []string
	finalized bool
	abortErr  error
}

func (w *mockStreamWriter) WriteChunk(text string) error {
	w.chunks = append(w.chunks, text)
	return nil
}

func (w *mockStreamWriter) WriteReasoning(_ string) error { return nil }

func (w *mockStreamWriter) Finalize() error {
	w.finalized = true
	return nil
}

func (w *mockStreamWriter) Abort(err error) error {
	w.abortErr = err
	return nil
}

type muxMockStreamChannel struct {
	muxMockChannel
	writer *mockStreamWriter
}

func (m *muxMockStreamChannel) BeginStream(_ context.Context, _ string) (StreamWriter, error) {
	return m.writer, nil
}

// --------------------------------------------------------------------------
// Compile-time assertion
// --------------------------------------------------------------------------

func TestMultiplexChannel_ImplementsStreamSender(t *testing.T) {
	var _ StreamSender = (*MultiplexChannel)(nil)
}

// --------------------------------------------------------------------------
// T1: Mux delegates to sub-channel that supports StreamSender
// --------------------------------------------------------------------------

func TestMuxStream_DelegatesToStreamingChild(t *testing.T) {
	writer := &mockStreamWriter{}
	streamCh := &muxMockStreamChannel{
		muxMockChannel: muxMockChannel{name: "cli"},
		writer:         writer,
	}
	mux := NewMultiplexChannel([]Channel{streamCh})

	sw, err := mux.BeginStream(context.Background(), "cli")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}

	if err := sw.WriteChunk("hello"); err != nil {
		t.Fatalf("WriteChunk() error: %v", err)
	}
	if err := sw.Finalize(); err != nil {
		t.Fatalf("Finalize() error: %v", err)
	}

	if len(writer.chunks) != 1 || writer.chunks[0] != "hello" {
		t.Errorf("chunks = %v, want [hello]", writer.chunks)
	}
	if !writer.finalized {
		t.Error("expected writer to be finalized")
	}
}

// --------------------------------------------------------------------------
// T2: Mux delegates with prefix matching (channelID = "cli:session123")
// --------------------------------------------------------------------------

func TestMuxStream_DelegatesWithPrefix(t *testing.T) {
	writer := &mockStreamWriter{}
	streamCh := &muxMockStreamChannel{
		muxMockChannel: muxMockChannel{name: "cli"},
		writer:         writer,
	}
	mux := NewMultiplexChannel([]Channel{streamCh})

	sw, err := mux.BeginStream(context.Background(), "cli:session123")
	if err != nil {
		t.Fatalf("BeginStream() error: %v", err)
	}
	if sw == nil {
		t.Fatal("expected non-nil StreamWriter")
	}
}

// --------------------------------------------------------------------------
// T3: Sub-channel doesn't support StreamSender → ErrStreamNotSupported
// --------------------------------------------------------------------------

func TestMuxStream_NonStreamingChild_ReturnsError(t *testing.T) {
	plainCh := &muxMockChannel{name: "cron"}
	mux := NewMultiplexChannel([]Channel{plainCh})

	_, err := mux.BeginStream(context.Background(), "cron:abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrStreamNotSupported) {
		t.Errorf("expected ErrStreamNotSupported, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// T4: No channel matches channelID → error
// --------------------------------------------------------------------------

func TestMuxStream_NoMatch_ReturnsError(t *testing.T) {
	plainCh := &muxMockChannel{name: "cli"}
	mux := NewMultiplexChannel([]Channel{plainCh})

	_, err := mux.BeginStream(context.Background(), "telegram:999")
	if err == nil {
		t.Fatal("expected error for unknown channelID")
	}
	if errors.Is(err, ErrStreamNotSupported) {
		t.Error("should NOT be ErrStreamNotSupported; channel wasn't found at all")
	}
}
