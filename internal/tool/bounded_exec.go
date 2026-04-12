// Package tool provides execution and utility tooling for the microagent.
package tool

// BoundedExec provides byte-limit and timeout enforcement for subprocess execution.
// It captures the first KeepFirstN lines (head) and last KeepLastN lines (tail) of
// combined stdout+stderr so that relevant context is preserved even when output is
// truncated.
//
// IMPORTANT: BoundedExec provides byte-limit and timeout enforcement only; it does
// NOT provide filesystem, network, process, or privilege isolation. Commands run
// with the same OS privileges as the calling process.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ExecErrorKind classifies the failure mode of a command invocation.
type ExecErrorKind string

const (
	// ExecErrorNone indicates a successful execution (check ExitCode for non-zero exits).
	ExecErrorNone ExecErrorKind = ""
	// ExecErrorStart indicates cmd.Start failed (command not found, permission denied, etc.).
	ExecErrorStart ExecErrorKind = "start"
	// ExecErrorTimeout indicates the command was killed by a context.WithTimeout expiration.
	ExecErrorTimeout ExecErrorKind = "timeout"
	// ExecErrorKilled indicates the command was killed by a signal (parent cancellation or external signal).
	ExecErrorKilled ExecErrorKind = "killed"
	// ExecErrorOther covers any other cmd.Run error not classified above.
	ExecErrorOther ExecErrorKind = "other"
)

// BoundedExecMetrics contains execution metrics from a BoundedExec.Run call.
type BoundedExecMetrics struct {
	StdoutBytes int
	StderrBytes int
	ExitCode    int
	Duration    time.Duration
	Truncated   bool
	// ErrorKind classifies the error when the exec itself failed.
	// ExecErrorNone for normal exits (including non-zero exit codes).
	ErrorKind ExecErrorKind
}

// BoundedExecResult contains the result of a bounded execution.
type BoundedExecResult struct {
	// Summary is the full output when not truncated, or head+truncation-notice+tail
	// when truncated.
	Summary string
	Metrics BoundedExecMetrics
}

// BoundedExec executes commands with output byte-limits and timeout enforcement.
type BoundedExec struct {
	MaxOutputBytes int
	Timeout        time.Duration
	KeepFirstN     int // lines to keep from head (default 20)
	KeepLastN      int // lines to keep from tail (default 10)
}

// headWriter captures the first keepFirstN lines and is always error-free.
// Once keepFirstN lines have been captured, additional writes are silently ignored
// but still return (len(p), nil) so the upstream io.Copy continues uninterrupted.
//
// NOT goroutine-safe on its own; callers must synchronise externally.
type headWriter struct {
	keepFirstN int
	lineCount  int
	holdover   []byte // bytes of the current incomplete line
	lines      [][]byte
}

func (h *headWriter) Write(p []byte) (int, error) {
	total := len(p)
	if h.lineCount >= h.keepFirstN {
		return total, nil
	}

	h.holdover = append(h.holdover, p...)
	for {
		idx := bytes.IndexByte(h.holdover, '\n')
		if idx < 0 {
			break
		}
		line := make([]byte, idx+1)
		copy(line, h.holdover[:idx+1])
		h.holdover = h.holdover[idx+1:]

		if h.lineCount < h.keepFirstN {
			h.lines = append(h.lines, line)
			h.lineCount++
		}
		if h.lineCount >= h.keepFirstN {
			h.holdover = nil
			break
		}
	}
	return total, nil
}

// content returns the captured head lines as a single string.
func (h *headWriter) content() string {
	var b strings.Builder
	for _, line := range h.lines {
		b.Write(line)
	}
	if len(h.holdover) > 0 && h.lineCount < h.keepFirstN {
		b.Write(h.holdover)
	}
	return b.String()
}

// tailRing captures the last keepLastN completed lines using a ring buffer.
// It is always error-free.
//
// NOT goroutine-safe on its own; callers must synchronise externally.
type tailRing struct {
	keepLastN int
	ring      [][]byte
	pos       int
	filled    bool
	holdover  []byte
	total     int
}

func newTailRing(keepLastN int) *tailRing {
	if keepLastN <= 0 {
		keepLastN = 1
	}
	return &tailRing{
		keepLastN: keepLastN,
		ring:      make([][]byte, keepLastN),
	}
}

func (r *tailRing) Write(p []byte) (int, error) {
	total := len(p)
	r.holdover = append(r.holdover, p...)
	for {
		idx := bytes.IndexByte(r.holdover, '\n')
		if idx < 0 {
			break
		}
		line := make([]byte, idx+1)
		copy(line, r.holdover[:idx+1])
		r.holdover = r.holdover[idx+1:]

		r.ring[r.pos] = line
		r.pos = (r.pos + 1) % r.keepLastN
		r.total++
		if r.total >= r.keepLastN {
			r.filled = true
		}
	}
	return total, nil
}

// content returns the captured tail lines in order (oldest first).
func (r *tailRing) content() string {
	var b strings.Builder
	if !r.filled {
		for i := 0; i < r.pos; i++ {
			if r.ring[i] != nil {
				b.Write(r.ring[i])
			}
		}
	} else {
		for i := 0; i < r.keepLastN; i++ {
			idx := (r.pos + i) % r.keepLastN
			if r.ring[idx] != nil {
				b.Write(r.ring[idx])
			}
		}
	}
	if len(r.holdover) > 0 {
		b.Write(r.holdover)
	}
	return b.String()
}

// boundedBuffer is an error-free writer that captures bytes up to maxBytes.
// Writes beyond maxBytes are silently dropped but the caller always sees success.
// Not goroutine-safe on its own; external synchronisation required.
type boundedBuffer struct {
	maxBytes int
	buf      bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if b.buf.Len() >= b.maxBytes {
		return total, nil
	}
	remaining := b.maxBytes - b.buf.Len()
	if len(p) <= remaining {
		b.buf.Write(p)
	} else {
		b.buf.Write(p[:remaining])
	}
	return total, nil
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

// byteCounter tracks total bytes seen and sets truncated once bytes exceed maxBytes.
// It is always error-free.
//
// NOT goroutine-safe on its own; callers must synchronise externally.
type byteCounter struct {
	maxBytes  int
	total     int
	truncated bool
}

func (c *byteCounter) Write(p []byte) (int, error) {
	c.total += len(p)
	if c.total > c.maxBytes {
		c.truncated = true
	}
	return len(p), nil
}

// stdStreamCountingWriter tracks per-stream byte counts for StdoutBytes/StderrBytes.
// Each stream has its own instance, so no mutex is needed between them.
// The combined writer below uses a mutex for the shared head/tail/counter writers.
type stdStreamCountingWriter struct {
	mu    sync.Mutex
	count int
}

func (s *stdStreamCountingWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.count += len(p)
	s.mu.Unlock()
	return len(p), nil
}

// mutexWriter wraps an io.Writer with a mutex for concurrent access.
// exec.Cmd runs stdout and stderr copying in separate goroutines; the combined
// writer (head+tail+counter) must be protected to avoid data races.
type mutexWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (m *mutexWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w.Write(p)
}

// Run executes the named command with the given args under the configured limits.
// It always returns nil error; exec errors are surfaced via BoundedExecMetrics.ErrorKind.
func (b *BoundedExec) Run(ctx context.Context, name string, args ...string) (BoundedExecResult, error) {
	keepFirst := b.KeepFirstN
	keepLast := b.KeepLastN
	if keepFirst <= 0 {
		keepFirst = 20
	}
	if keepLast <= 0 {
		keepLast = 10
	}

	// Apply timeout.
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

	// Four independent, error-free writers for combined output.
	head := &headWriter{keepFirstN: keepFirst}
	tail := newTailRing(keepLast)
	counter := &byteCounter{maxBytes: b.MaxOutputBytes}
	mainBuf := &boundedBuffer{maxBytes: b.MaxOutputBytes}

	// io.MultiWriter propagates writes to all four writers; all return nil error so
	// io.Copy inside exec.Cmd always sees success and runs to completion.
	// The mutexWriter serialises concurrent writes from stdout and stderr goroutines.
	combined := &mutexWriter{w: io.MultiWriter(head, tail, counter, mainBuf)}

	// Per-stream byte counters protect themselves individually.
	var stdoutCounter, stderrCounter stdStreamCountingWriter

	cmd.Stdout = io.MultiWriter(&stdoutCounter, combined)
	cmd.Stderr = io.MultiWriter(&stderrCounter, combined)

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	// Classify error kind.
	errorKind := classifyExecError(ctx, runErr)

	// For ExecErrorStart the process never started, so ProcessState is nil.
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	metrics := BoundedExecMetrics{
		StdoutBytes: stdoutCounter.count,
		StderrBytes: stderrCounter.count,
		ExitCode:    exitCode,
		Duration:    duration,
		Truncated:   counter.truncated,
		ErrorKind:   errorKind,
	}

	summary := b.extractSummary(head, tail, counter, mainBuf, metrics)

	return BoundedExecResult{
		Summary: summary,
		Metrics: metrics,
	}, nil
}

// classifyExecError maps cmd.Run errors to ExecErrorKind values.
//
//   - nil                            → ExecErrorNone  (normal exit, possibly non-zero)
//   - context DeadlineExceeded       → ExecErrorTimeout
//   - exec.ErrNotFound / ErrNotExist → ExecErrorStart  (command not found)
//   - *exec.ExitError, signal-killed → ExecErrorKilled
//   - *exec.ExitError, normal exit   → ExecErrorNone  (non-zero exit code, not a fault)
//   - anything else                  → ExecErrorOther
func classifyExecError(ctx context.Context, runErr error) ExecErrorKind {
	if runErr == nil {
		return ExecErrorNone
	}
	// Timeout takes priority over signal kill because the timeout kills via signal.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ExecErrorTimeout
	}
	// exec.ErrNotFound covers PATH lookup failures; os.ErrNotExist covers absolute path failures.
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, os.ErrNotExist) {
		return ExecErrorStart
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ProcessState != nil && !exitErr.ProcessState.Exited() {
			// Process was killed by a signal before it could exit normally.
			return ExecErrorKilled
		}
		// Process exited normally but with a non-zero status — not a fault.
		return ExecErrorNone
	}
	return ExecErrorOther
}

// extractSummary builds a human-readable summary from the captured writers.
// When not truncated, mainBuf holds the complete bounded output and is returned directly.
// When truncated, combineHeadTail formats a head+notice+tail view.
func (b *BoundedExec) extractSummary(head *headWriter, tail *tailRing, counter *byteCounter, mainBuf *boundedBuffer, metrics BoundedExecMetrics) string {
	if !metrics.Truncated {
		return mainBuf.String()
	}
	headContent := head.content()
	tailContent := tail.content()
	return b.combineHeadTail(headContent, tailContent, metrics)
}

// combineHeadTail merges head and tail content into a truncation summary string.
// This function is only called when metrics.Truncated == true.
func (b *BoundedExec) combineHeadTail(headContent, tailContent string, metrics BoundedExecMetrics) string {
	// Truncated: show head + notice + tail.
	truncationNotice := fmt.Sprintf("\n...\n[truncated: %d/%d bytes, %v]\n...\n",
		metrics.StdoutBytes+metrics.StderrBytes, b.MaxOutputBytes, metrics.Duration.Round(time.Millisecond))

	headTrimmed := strings.TrimSuffix(headContent, "\n")
	tailTrimmed := strings.TrimPrefix(tailContent, "\n")

	if headTrimmed == "" && tailTrimmed == "" {
		return "[no output]"
	}
	if headTrimmed == "" {
		return tailTrimmed
	}
	if tailTrimmed == "" {
		return headTrimmed + truncationNotice
	}
	return headTrimmed + truncationNotice + tailTrimmed
}

