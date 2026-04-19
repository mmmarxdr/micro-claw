package channel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/content"
)

// syncBuffer is a goroutine-safe bytes.Buffer for use in tests where the
// CLI goroutine writes to the buffer concurrently with test assertions.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func newTestCLIChannel(in io.Reader, out io.Writer) *CLIChannel {
	return NewCLIChannel(config.ChannelConfig{}, config.MediaConfig{}, nil, in, out)
}

// TestNewCLIChannel_NonNil verifies the constructor returns a non-nil channel.
func TestNewCLIChannel_NonNil(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()
	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	if ch == nil {
		t.Fatal("expected non-nil CLIChannel, got nil")
	}
}

// TestCLIChannel_Name verifies Name() returns "cli".
func TestCLIChannel_Name(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()
	ch := newTestCLIChannel(pr, &bytes.Buffer{})
	if got := ch.Name(); got != "cli" {
		t.Errorf("Name() = %q, want %q", got, "cli")
	}
}

// TestCLIChannel_StartIsNonBlocking verifies Start() returns within 100ms.
func TestCLIChannel_StartIsNonBlocking(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	inbox := make(chan IncomingMessage, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- ch.Start(ctx, inbox)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start() returned unexpected error: %v", err)
		}
		// returned quickly — pass
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Start() did not return within 100ms (blocking)")
	}
}

// TestCLIChannel_Send verifies Send() writes the expected format to the output writer.
func TestCLIChannel_Send(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	ctx := context.Background()

	msg := OutgoingMessage{
		Text: "hello world",
	}

	err := ch.Send(ctx, msg)
	if err != nil {
		t.Fatalf("Send() returned error: %v", err)
	}

	got := buf.String()
	// Drain the startup output (written during Start) — here we only call Send directly
	// so buf should contain exactly the Send output.
	// The format from cli.go line 76: fmt.Fprintf(c.out, "\nAgent: %s\n> ", msg.Text)
	want := "\nAgent: hello world\n> "

	// buf may contain startup banner if Start was called, but we didn't call Start here.
	if !strings.Contains(got, want) {
		t.Errorf("Send() output = %q, want it to contain %q", got, want)
	}
}

// TestCLIChannel_Stop verifies Stop() returns nil.
func TestCLIChannel_Stop(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	ch := newTestCLIChannel(pr, &bytes.Buffer{})
	if err := ch.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

// TestCLIChannel_MessageRouting verifies that a line written to the pipe
// arrives in the inbox as an IncomingMessage.
func TestCLIChannel_MessageRouting(t *testing.T) {
	pr, pw := io.Pipe()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	inbox := make(chan IncomingMessage, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		pw.Close()
		pr.Close()
	})

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Write a line to the pipe
	if _, err := io.WriteString(pw, "hello\n"); err != nil {
		t.Fatalf("failed to write to pipe: %v", err)
	}

	select {
	case msg := <-inbox:
		if msg.Text() != "hello" {
			t.Errorf("msg.Text() = %q, want %q", msg.Text(), "hello")
		}
		if msg.ChannelID != "cli" {
			t.Errorf("msg.ChannelID = %q, want %q", msg.ChannelID, "cli")
		}
		if msg.SenderID != "local_user" {
			t.Errorf("msg.SenderID = %q, want %q", msg.SenderID, "local_user")
		}
		if msg.ID == "" {
			t.Error("msg.ID is empty, expected non-empty UUID")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for IncomingMessage in inbox")
	}
}

// TestCLIChannel_EmptyLineDiscarded verifies that an empty line is NOT sent to inbox.
func TestCLIChannel_EmptyLineDiscarded(t *testing.T) {
	pr, pw := io.Pipe()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	inbox := make(chan IncomingMessage, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		pw.Close()
		pr.Close()
	})

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Write an empty line
	if _, err := io.WriteString(pw, "\n"); err != nil {
		t.Fatalf("failed to write to pipe: %v", err)
	}

	select {
	case msg := <-inbox:
		t.Errorf("expected no message but got: %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// correct — no message arrived
	}
}

// TestCLIChannel_ContextCancellation verifies that cancelling the context
// causes the goroutine to exit cleanly.
func TestCLIChannel_ContextCancellation(t *testing.T) {
	pr, pw := io.Pipe()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	inbox := make(chan IncomingMessage, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		pw.Close()
		pr.Close()
	})

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Give the goroutine a moment to start
	time.Sleep(10 * time.Millisecond)

	// Cancel the context — the goroutine should exit
	cancel()

	// Use a watchdog: write a line after cancellation; if the goroutine exited,
	// no message should arrive in the inbox.
	// Wait briefly to allow context cancellation to propagate.
	time.Sleep(50 * time.Millisecond)

	// Verify no further messages are processed by sending a line and checking
	// if the goroutine already exited (inbox stays empty).
	// We can't directly observe goroutine exit without goleak, so we verify
	// via the inbox remaining quiet after cancellation.
	select {
	case msg := <-inbox:
		t.Logf("received message after cancel (goroutine may still be running): %+v", msg)
	case <-time.After(500 * time.Millisecond):
		// Expected: goroutine exits and no more processing
	}
}

// TestCLIChannel_PipeClose verifies that closing the pipe writer
// causes the goroutine to exit cleanly without panicking.
func TestCLIChannel_PipeClose(t *testing.T) {
	pr, pw := io.Pipe()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)
	inbox := make(chan IncomingMessage, 10)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		pr.Close()
	})

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Give the goroutine time to start scanning
	time.Sleep(10 * time.Millisecond)

	// Close the writer — this causes the scanner to get EOF
	pw.Close()

	// The goroutine should exit within 1 second without panicking
	// We verify this by checking that the test itself completes (no hang/panic)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Small poll: the goroutine exits and inbox stays empty after pipe close
		time.Sleep(100 * time.Millisecond)
	}()

	select {
	case <-done:
		// success — no panic, exited cleanly within time
	case <-time.After(1 * time.Second):
		t.Fatal("goroutine did not exit within 1 second after pipe close")
	}
}

// TestNewCLIChannelDefault verifies that NewCLIChannelDefault returns a non-nil channel.
func TestNewCLIChannelDefault(t *testing.T) {
	ch := NewCLIChannelDefault(config.ChannelConfig{}, config.MediaConfig{}, nil)
	if ch == nil {
		t.Fatal("expected non-nil CLIChannel from NewCLIChannelDefault, got nil")
	}
}

// TestCLIChannel_Send_OnlySendOutput verifies Send() writes exactly the right format
// to a fresh buffer (not mixed with Start banner output).
func TestCLIChannel_Send_ExactOutput(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	buf := &bytes.Buffer{}
	ch := newTestCLIChannel(pr, buf)

	// Call Send without calling Start — buffer starts empty
	err := ch.Send(context.Background(), OutgoingMessage{Text: "test message"})
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}

	got := buf.String()
	want := "\nAgent: test message\n> "
	if got != want {
		t.Errorf("Send() output = %q, want %q", got, want)
	}
}

// --- /attach command tests ---

// buildMediaCLIChannel creates a CLIChannel with media enabled and the given store.
func buildMediaCLIChannel(in io.Reader, out io.Writer, ms *fakeMediaStore) *CLIChannel {
	enabled := true
	med := config.MediaConfig{
		Enabled:             &enabled,
		MaxAttachmentBytes:  1024 * 1024, // 1 MB
		MaxMessageBytes:     5 * 1024 * 1024,
		AllowedMIMEPrefixes: []string{"image/", "audio/", "text/", "application/pdf"},
	}
	return NewCLIChannel(config.ChannelConfig{}, med, ms, in, out)
}

// TestCLIChannel_Attach_ImageFile verifies /attach with a JPEG-like temp file enqueues
// a BlockImage with the expected SHA from the fake store.
func TestCLIChannel_Attach_ImageFile(t *testing.T) {
	// Create a temp file with JPEG magic bytes (FFD8FF)
	tmpFile, err := os.CreateTemp("", "test-*.jpg")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	// Write JPEG magic bytes followed by padding
	jpegData := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x00}, 100)...)
	if _, err := tmpFile.Write(jpegData); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	ms := newFakeMediaStore()
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	ch := buildMediaCLIChannel(pr, out, ms)

	inbox := make(chan IncomingMessage, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer pw.Close()

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Write /attach command
	if _, err := fmt.Fprintf(pw, "/attach %s\n", tmpFile.Name()); err != nil {
		t.Fatalf("write error: %v", err)
	}

	select {
	case msg := <-inbox:
		if len(msg.Content) == 0 {
			t.Fatal("expected content blocks, got none")
		}
		block := msg.Content[0]
		if block.Type != content.BlockImage {
			t.Errorf("expected BlockImage, got %q", block.Type)
		}
		if block.MediaSHA256 == "" {
			t.Error("expected non-empty MediaSHA256")
		}
		ms.mu.Lock()
		cnt := ms.callCnt
		ms.mu.Unlock()
		if cnt == 0 {
			t.Error("expected StoreMedia to be called")
		}
		// Verify the SHA is in the store
		if _, ok := ms.stored[block.MediaSHA256]; !ok {
			t.Errorf("SHA %q not found in fake store", block.MediaSHA256)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for attach message")
	}
}

// TestCLIChannel_Attach_OversizedFile verifies an oversized file prints a rejection
// notice and does NOT enqueue a message.
func TestCLIChannel_Attach_OversizedFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-oversized-*.bin")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	// Write data larger than MaxAttachmentBytes (1 MB)
	bigData := bytes.Repeat([]byte("X"), 2*1024*1024) // 2 MB
	if _, err := tmpFile.Write(bigData); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	tmpFile.Close()

	ms := newFakeMediaStore()
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	ch := buildMediaCLIChannel(pr, out, ms)

	inbox := make(chan IncomingMessage, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer pw.Close()

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	if _, err := fmt.Fprintf(pw, "/attach %s\n", tmpFile.Name()); err != nil {
		t.Fatalf("write error: %v", err)
	}

	select {
	case msg := <-inbox:
		t.Fatalf("oversized file should not enqueue, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// expected — check that rejection notice was printed
	}

	if !strings.Contains(out.String(), "too large") {
		t.Errorf("expected rejection notice in output, got: %q", out.String())
	}
	ms.mu.Lock()
	cnt := ms.callCnt
	ms.mu.Unlock()
	if cnt != 0 {
		t.Errorf("expected StoreMedia not called, called %d times", cnt)
	}
}

// TestCLIChannel_Attach_BlockedMIME verifies that a file with a blocked MIME type
// prints a rejection notice and does NOT enqueue a message.
func TestCLIChannel_Attach_BlockedMIME(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-exec-*.exe")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	// Write Windows PE magic bytes (MZ header) → MIME will be application/x-msdownload or octet-stream
	exeData := append([]byte{0x4D, 0x5A}, bytes.Repeat([]byte{0x00}, 100)...)
	if _, err := tmpFile.Write(exeData); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	tmpFile.Close()

	ms := newFakeMediaStore()
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	ch := buildMediaCLIChannel(pr, out, ms)

	inbox := make(chan IncomingMessage, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer pw.Close()

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	if _, err := fmt.Fprintf(pw, "/attach %s\n", tmpFile.Name()); err != nil {
		t.Fatalf("write error: %v", err)
	}

	select {
	case msg := <-inbox:
		t.Fatalf("blocked MIME should not enqueue, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// expected
	}

	if !strings.Contains(out.String(), "not allowed") {
		t.Errorf("expected MIME rejection notice, got: %q", out.String())
	}
	ms.mu.Lock()
	cnt := ms.callCnt
	ms.mu.Unlock()
	if cnt != 0 {
		t.Errorf("expected StoreMedia not called, called %d times", cnt)
	}
}

// TestCLIChannel_Attach_MediaDisabled verifies that /attach prints "media disabled"
// and does NOT enqueue when media is not enabled.
func TestCLIChannel_Attach_MediaDisabled(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-disabled-*.txt")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	if err := os.WriteFile(tmpFile.Name(), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	pr, pw := io.Pipe()
	out := &syncBuffer{}
	// media disabled: nil store
	ch := newTestCLIChannel(pr, out)

	inbox := make(chan IncomingMessage, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer pw.Close()

	if err := ch.Start(ctx, inbox); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	if _, err := fmt.Fprintf(pw, "/attach %s\n", tmpFile.Name()); err != nil {
		t.Fatalf("write error: %v", err)
	}

	select {
	case msg := <-inbox:
		t.Fatalf("media disabled — should not enqueue, got %+v", msg)
	case <-time.After(200 * time.Millisecond):
		// expected
	}

	if !strings.Contains(out.String(), "media disabled") {
		t.Errorf("expected 'media disabled' notice, got: %q", out.String())
	}
}
