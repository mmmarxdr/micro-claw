package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var ErrMaxBytes = errors.New("max output bytes reached")

// countingWriter wraps an io.Writer and counts bytes written.
// Returns ErrMaxBytes when the maxBytes limit is hit.
type countingWriter struct {
	writer       io.Writer
	maxBytes     int
	bytesWritten int
	truncated    bool
	mu           sync.Mutex
}

func (cw *countingWriter) Write(p []byte) (n int, err error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	// If already truncated, don't write anything
	if cw.truncated {
		return 0, ErrMaxBytes
	}

	// Check if this write would exceed the limit
	if cw.bytesWritten+len(p) > cw.maxBytes {
		// Write only up to the limit
		remaining := cw.maxBytes - cw.bytesWritten
		if remaining > 0 {
			n, err = cw.writer.Write(p[:remaining])
			if err == nil {
				cw.bytesWritten += n
			}
		}
		cw.truncated = true
		// Return ErrMaxBytes to indicate truncation happened
		if remaining > 0 && err == nil {
			return n, ErrMaxBytes
		}
		return 0, ErrMaxBytes
	}

	n, err = cw.writer.Write(p)
	if err == nil {
		cw.bytesWritten += n
	}
	return n, err
}

// sequentialWriter writes to multiple writers sequentially.
// Unlike io.MultiWriter, it continues writing to subsequent writers
// even if a previous writer returns an error.
type sequentialWriter struct {
	writers []io.Writer
}

func (sw *sequentialWriter) Write(p []byte) (n int, err error) {
	for _, w := range sw.writers {
		wn, werr := w.Write(p)
		if wn > n {
			n = wn
		}
		if werr != nil && err == nil {
			err = werr
		}
	}
	return n, err
}

// SandboxMetrics contains execution metrics.
type SandboxMetrics struct {
	StdoutBytes int
	StderrBytes int
	ExitCode    int
	Duration    time.Duration
	Truncated   bool
}

// SandboxResult contains the result of a sandboxed execution.
type SandboxResult struct {
	Summary string // first N lines + last N lines + metrics footer
	Output  string // full output (used by batch_exec indexing)
	Metrics SandboxMetrics
}

// Sandbox wraps exec.Command with output limits and timeout enforcement.
type Sandbox struct {
	MaxOutputBytes int
	Timeout        time.Duration
	KeepFirstN     int // lines to keep from head (default 20)
	KeepLastN      int // lines to keep from tail (default 10)
}

// Run executes a command in the sandbox with output limits and timeout.
func (s *Sandbox) Run(ctx context.Context, name string, args ...string) (SandboxResult, error) {
	// Apply timeout
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

	// Create buffers for stdout and stderr
	var stdoutBuf, stderrBuf bytes.Buffer
	var combinedBuf bytes.Buffer

	// Create writers: stdout/stderr -> combined buffer -> counting writers
	// We write to combinedBuf first so it gets all output even when counting writers truncate
	stdoutCW := &countingWriter{writer: &stdoutBuf, maxBytes: s.MaxOutputBytes}
	stderrCW := &countingWriter{writer: &stderrBuf, maxBytes: s.MaxOutputBytes}

	// Create a writer that writes to combinedBuf, then to counting writer
	// We need a custom writer since io.MultiWriter stops on first error
	stdoutWriter := &sequentialWriter{writers: []io.Writer{&combinedBuf, stdoutCW}}
	stderrWriter := &sequentialWriter{writers: []io.Writer{&combinedBuf, stderrCW}}

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	start := time.Now()
	cmd.Run() // Error handled below via ProcessState
	duration := time.Since(start)

	// Extract metrics
	metrics := SandboxMetrics{
		Duration: duration,
	}

	// Determine if truncated (check counting writers)
	truncated := false
	if stdoutCW != nil && stdoutCW.truncated {
		truncated = true
	}
	if stderrCW != nil && stderrCW.truncated {
		truncated = true
	}
	metrics.Truncated = truncated

	// Get stdout/stderr byte counts
	stdoutBytes := stdoutBuf.Len()
	stderrBytes := stderrBuf.Len()
	metrics.StdoutBytes = stdoutBytes
	metrics.StderrBytes = stderrBytes

	// Get exit code
	if cmd.ProcessState != nil {
		metrics.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		metrics.ExitCode = -1
	}

	// Build summary
	output := combinedBuf.String()
	summary := s.extractSummary(output, truncated, metrics)

	return SandboxResult{
		Summary: summary,
		Output:  output,
		Metrics: metrics,
	}, nil
}

// extractSummary builds a summary from the output.
// If truncated, returns first KeepFirstN lines + truncation notice + last KeepLastN lines.
// Otherwise returns the full output.
func (s *Sandbox) extractSummary(output string, truncated bool, metrics SandboxMetrics) string {
	if !truncated {
		return output
	}

	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "[no output]"
	}

	keepFirst := s.KeepFirstN
	keepLast := s.KeepLastN
	if keepFirst <= 0 {
		keepFirst = 20
	}
	if keepLast <= 0 {
		keepLast = 10
	}

	// Always show truncation notice when truncated, even for short output
	truncationNotice := fmt.Sprintf("\n...\n[truncated: %d/%d bytes, %v]\n...\n",
		len(output), s.MaxOutputBytes, metrics.Duration.Round(time.Millisecond))

	if len(lines) <= keepFirst+keepLast {
		// Output is short enough to show entirely, but still show truncation notice
		return output + truncationNotice
	}

	head := lines[:min(keepFirst, len(lines))]
	tail := lines[max(0, len(lines)-keepLast):]

	return strings.Join(head, "\n") + truncationNotice + strings.Join(tail, "\n")
}
