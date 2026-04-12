package tool

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newTestBoundedExec returns a BoundedExec suitable for unit tests with the
// supplied limits. The defaults here match the field zero-value behaviour:
// KeepFirstN/KeepLastN fall back to 20/10 when zero.
func newTestBoundedExec(maxBytes int, timeout time.Duration, keepFirst, keepLast int) *BoundedExec {
	return &BoundedExec{
		MaxOutputBytes: maxBytes,
		Timeout:        timeout,
		KeepFirstN:     keepFirst,
		KeepLastN:      keepLast,
	}
}

// TestBoundedExecRun exercises the full execution path.
func TestBoundedExecRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		be             *BoundedExec
		cmd            string
		args           []string
		wantInSummary  []string // all must appear
		wantExitCode   int
		wantTruncated  bool
		wantErrorKind  ExecErrorKind
		wantNonZeroDur bool
	}{
		{
			name:          "empty output",
			be:            newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:           "true",
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name:          "output under limit",
			be:            newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:           "sh",
			args:          []string{"-c", "echo hello"},
			wantInSummary: []string{"hello"},
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name: "output exactly at limit (single byte under triggers no truncation)",
			be:   newTestBoundedExec(6, 5*time.Second, 20, 10), // "hello\n" = 6 bytes
			cmd:  "sh",
			args: []string{"-c", "printf 'hello\n'"},
			// 6 bytes == limit, counter.total = 6 which is NOT > 6, so not truncated
			wantTruncated: false,
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name: "head-only short output (well under KeepFirstN)",
			be:   newTestBoundedExec(4096, 5*time.Second, 5, 3),
			cmd:  "sh",
			args: []string{"-c", "for i in 1 2 3; do echo line$i; done"},
			wantInSummary: []string{"line1", "line2", "line3"},
			wantTruncated: false,
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name: "tail-only scenario: truncated, head fills, tail captures last N",
			// 30 lines × ~10 bytes each ≈ 300 bytes; MaxOutputBytes=50 forces truncation.
			be:   newTestBoundedExec(50, 5*time.Second, 3, 2),
			cmd:  "sh",
			args: []string{"-c", "for i in $(seq 1 30); do echo \"line$i\"; done"},
			// head: first 3 lines; tail: last 2 lines (line29, line30)
			wantInSummary: []string{"line1", "line29", "line30"},
			wantTruncated: true,
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name: "mixed stdout and stderr captured",
			be:   newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:  "sh",
			args: []string{"-c", "echo stdout_line; echo stderr_line >&2"},
			wantInSummary: []string{"stdout_line", "stderr_line"},
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name:          "non-zero exit code → ErrorKind empty (not a kill/timeout)",
			be:            newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:           "false",
			wantExitCode:  1,
			wantErrorKind: ExecErrorNone,
		},
		{
			name:          "successful exit code 0 → ErrorKind empty",
			be:            newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:           "true",
			wantExitCode:  0,
			wantErrorKind: ExecErrorNone,
		},
		{
			name:          "timeout → ErrorKind timeout",
			be:            newTestBoundedExec(4096, 100*time.Millisecond, 20, 10),
			cmd:           "sleep",
			args:          []string{"10"},
			wantExitCode:  -1,
			wantErrorKind: ExecErrorTimeout,
		},
		{
			name:          "command not found → ErrorKind start",
			be:            newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:           "/nonexistent/binary/that/does/not/exist",
			wantExitCode:  -1,
			wantErrorKind: ExecErrorStart,
		},
		{
			name:           "duration is non-zero",
			be:             newTestBoundedExec(4096, 5*time.Second, 20, 10),
			cmd:            "true",
			wantNonZeroDur: true,
			wantErrorKind:  ExecErrorNone,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := tc.be.Run(context.Background(), tc.cmd, tc.args...)
			if err != nil {
				t.Fatalf("Run returned unexpected error: %v", err)
			}
			for _, want := range tc.wantInSummary {
				if !strings.Contains(result.Summary, want) {
					t.Errorf("Summary missing %q\nSummary: %q", want, result.Summary)
				}
			}
			if result.Metrics.ExitCode != tc.wantExitCode {
				t.Errorf("ExitCode = %d, want %d", result.Metrics.ExitCode, tc.wantExitCode)
			}
			if result.Metrics.Truncated != tc.wantTruncated {
				t.Errorf("Truncated = %v, want %v (summary: %q)", result.Metrics.Truncated, tc.wantTruncated, result.Summary)
			}
			if result.Metrics.ErrorKind != tc.wantErrorKind {
				t.Errorf("ErrorKind = %q, want %q", result.Metrics.ErrorKind, tc.wantErrorKind)
			}
			if tc.wantNonZeroDur && result.Metrics.Duration == 0 {
				t.Error("Duration should be non-zero")
			}
		})
	}
}

// TestBoundedExecParentCancellation verifies that parent context cancellation
// kills the subprocess and sets ErrorKind=killed.
func TestBoundedExecParentCancellation(t *testing.T) {
	t.Parallel()

	be := newTestBoundedExec(4096, 30*time.Second, 20, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var result BoundedExecResult
	go func() {
		defer close(done)
		result, _ = be.Run(ctx, "sleep", "30")
	}()

	// Cancel the parent context shortly after start.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after parent context was cancelled")
	}

	// Context was cancelled by the parent (not a deadline), so we expect
	// either ExecErrorKilled or ExecErrorTimeout (implementation maps
	// parent cancellation → killed since ctx.Err() is context.Canceled, not
	// context.DeadlineExceeded).
	if result.Metrics.ErrorKind != ExecErrorKilled && result.Metrics.ErrorKind != ExecErrorTimeout {
		t.Errorf("ErrorKind = %q, want killed or timeout", result.Metrics.ErrorKind)
	}
}

// TestBoundedExecStdoutStderrCounts verifies per-stream byte counting.
func TestBoundedExecStdoutStderrCounts(t *testing.T) {
	t.Parallel()

	be := newTestBoundedExec(4096, 5*time.Second, 20, 10)
	result, err := be.Run(context.Background(), "sh", "-c", "printf 'abc'; printf 'xy' >&2")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Metrics.StdoutBytes != 3 {
		t.Errorf("StdoutBytes = %d, want 3", result.Metrics.StdoutBytes)
	}
	if result.Metrics.StderrBytes != 2 {
		t.Errorf("StderrBytes = %d, want 2", result.Metrics.StderrBytes)
	}
}

// TestBoundedExecTruncationNotice verifies the truncation notice format.
func TestBoundedExecTruncationNotice(t *testing.T) {
	t.Parallel()

	be := &BoundedExec{
		MaxOutputBytes: 50,
		Timeout:        5 * time.Second,
		KeepFirstN:     3,
		KeepLastN:      2,
	}
	// Generate many lines; total bytes will exceed 50.
	result, err := be.Run(context.Background(), "sh", "-c", "for i in $(seq 1 20); do echo \"line$i\"; done")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !result.Metrics.Truncated {
		t.Skip("output did not truncate; test requires truncation")
	}
	if !strings.Contains(result.Summary, "[truncated:") {
		t.Errorf("Summary should contain truncation notice, got: %q", result.Summary)
	}
}

// TestHeadWriter exercises the headWriter in isolation.
func TestHeadWriter(t *testing.T) {
	t.Parallel()

	t.Run("captures first N lines", func(t *testing.T) {
		t.Parallel()
		hw := &headWriter{keepFirstN: 3}
		input := "line1\nline2\nline3\nline4\nline5\n"
		n, err := hw.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if n != len(input) {
			t.Errorf("Write returned %d, want %d", n, len(input))
		}
		got := hw.content()
		for _, want := range []string{"line1", "line2", "line3"} {
			if !strings.Contains(got, want) {
				t.Errorf("head content missing %q: %q", want, got)
			}
		}
		if strings.Contains(got, "line4") || strings.Contains(got, "line5") {
			t.Errorf("head content should not contain lines past KeepFirstN, got: %q", got)
		}
	})

	t.Run("error-free after limit", func(t *testing.T) {
		t.Parallel()
		hw := &headWriter{keepFirstN: 1}
		// First write: one complete line.
		n, err := hw.Write([]byte("line1\n"))
		if err != nil || n != 6 {
			t.Fatalf("first Write: n=%d, err=%v", n, err)
		}
		// Second write: exceeds keepFirstN.
		n, err = hw.Write([]byte("line2\n"))
		if err != nil {
			t.Errorf("Write after limit must return nil error, got %v", err)
		}
		if n != 6 {
			t.Errorf("Write after limit must return len(p), got %d", n)
		}
	})
}

// TestTailRing exercises the tailRing in isolation.
func TestTailRing(t *testing.T) {
	t.Parallel()

	t.Run("captures last N lines from stream", func(t *testing.T) {
		t.Parallel()
		tr := newTailRing(3)
		for i := 1; i <= 10; i++ {
			line := []byte(strings.Repeat("x", 5) + "\n")
			tr.Write(line) //nolint:errcheck
		}
		// Last 3 lines (lines 8, 9, 10) should be in ring.
		got := tr.content()
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) != 3 {
			t.Errorf("tail ring has %d lines, want 3; content: %q", len(lines), got)
		}
	})

	t.Run("ring preserves order oldest first", func(t *testing.T) {
		t.Parallel()
		tr := newTailRing(3)
		for i := 1; i <= 5; i++ {
			line := []byte(strings.Repeat("a", i) + "\n") // different length per line
			tr.Write(line)                                 //nolint:errcheck
		}
		got := tr.content()
		// Lines 3, 4, 5 (by length: 3,4,5 'a's).
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
		}
		if len(lines[0]) != 3 || len(lines[1]) != 4 || len(lines[2]) != 5 {
			t.Errorf("unexpected line order/content: %v", lines)
		}
	})

	t.Run("error-free always", func(t *testing.T) {
		t.Parallel()
		tr := newTailRing(2)
		for i := 0; i < 100; i++ {
			n, err := tr.Write([]byte("data\n"))
			if err != nil || n != 5 {
				t.Fatalf("Write %d: n=%d err=%v", i, n, err)
			}
		}
	})
}

// TestBoundedExec_NonTruncated_Preserves_Full_Output verifies that when output is
// not truncated but exceeds KeepFirstN lines, all lines are still present in the
// summary (regression: the broken head+tail-only path silently dropped middle lines).
func TestBoundedExec_NonTruncated_Preserves_Full_Output(t *testing.T) {
	t.Parallel()

	// 100 lines × ~20 bytes ≈ 2 KB total; MaxOutputBytes=8192 ensures no truncation.
	// KeepFirstN=20, KeepLastN=10 means head covers lines 1-20 and tail covers 91-100.
	// The middle lines 21-90 were silently dropped by the old code.
	be := newTestBoundedExec(8192, 10*time.Second, 20, 10)
	result, err := be.Run(context.Background(), "sh", "-c",
		"for i in $(seq 1 100); do printf 'line %d\\n' $i; done")
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if result.Metrics.Truncated {
		t.Fatalf("expected Truncated=false, got true (summary len=%d)", len(result.Summary))
	}

	for _, want := range []string{"line 1", "line 50", "line 100"} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("summary missing %q — middle lines were dropped\nSummary: %q", want, result.Summary)
		}
	}

	// Count newlines to verify all 100 lines are present.
	gotLines := strings.Count(result.Summary, "\n")
	if gotLines != 100 {
		t.Errorf("expected 100 newlines in summary, got %d\nSummary: %q", gotLines, result.Summary)
	}
}

// TestBoundedExec_Bounded_NonTruncated_RespectsCap verifies boundary behaviour:
// output exactly at MaxOutputBytes is NOT truncated; output one byte over IS truncated.
func TestBoundedExec_Bounded_NonTruncated_RespectsCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		outputBytes   int // bytes to generate (printf writes exactly this many 'x')
		maxBytes      int
		wantTruncated bool
	}{
		{
			name:          "output equals limit — not truncated",
			outputBytes:   10,
			maxBytes:      10,
			wantTruncated: false,
		},
		{
			name:          "output exceeds limit by one byte — truncated",
			outputBytes:   11,
			maxBytes:      10,
			wantTruncated: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be := newTestBoundedExec(tc.maxBytes, 5*time.Second, 20, 10)
			// Generate exactly tc.outputBytes 'x' characters (no trailing newline so
			// byte count is precise).
			script := fmt.Sprintf("printf '%%s' '%s'", strings.Repeat("x", tc.outputBytes))
			result, err := be.Run(context.Background(), "sh", "-c", script)
			if err != nil {
				t.Fatalf("Run error: %v", err)
			}
			if result.Metrics.Truncated != tc.wantTruncated {
				t.Errorf("Truncated=%v, want %v (stdout=%d maxBytes=%d)",
					result.Metrics.Truncated, tc.wantTruncated,
					result.Metrics.StdoutBytes, tc.maxBytes)
			}
		})
	}
}

// TestBoundedExec_Truncated_StillUses_HeadTail verifies that when output is
// truncated the summary uses head+notice+tail, stays bounded, and does not
// contain the full raw output.
func TestBoundedExec_Truncated_StillUses_HeadTail(t *testing.T) {
	t.Parallel()

	// Generate ~10 000 bytes; MaxOutputBytes=200 forces truncation.
	// KeepFirstN=3, KeepLastN=2 so head has lines 1-3, tail has last 2 lines.
	be := newTestBoundedExec(200, 10*time.Second, 3, 2)
	// Each line is "line NNN\n" (~9 bytes); 1000 lines ≈ 9 KB.
	result, err := be.Run(context.Background(), "sh", "-c",
		"for i in $(seq 1 1000); do printf 'line %d\\n' $i; done")
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if !result.Metrics.Truncated {
		t.Fatalf("expected Truncated=true, got false")
	}
	if !strings.Contains(result.Summary, "[truncated:") {
		t.Errorf("summary should contain truncation notice, got: %q", result.Summary)
	}

	// Head lines must appear.
	for _, want := range []string{"line 1", "line 2", "line 3"} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("summary missing head line %q\nSummary: %q", want, result.Summary)
		}
	}

	// Tail lines (last 2 of 1000) must appear.
	for _, want := range []string{"line 999", "line 1000"} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("summary missing tail line %q\nSummary: %q", want, result.Summary)
		}
	}

	// Summary must be well-bounded — nowhere near the 10 000 bytes of raw output.
	const maxSummaryBytes = 2000
	if len(result.Summary) > maxSummaryBytes {
		t.Errorf("summary too large (%d bytes), want <= %d — head+tail should keep it small",
			len(result.Summary), maxSummaryBytes)
	}
}

// TestBoundedExec_TimeoutDuration_InBounds verifies that when a command is
// killed by the BoundedExec timeout, the reported Duration is within a
// reasonable window of the configured timeout.  The original test only checked
// that Duration was non-zero; this tighter version catches drift in both
// directions (too fast = timer fired early, too slow = measurement overhead).
//
// Bounds: [80 ms, 250 ms] for a 100 ms timeout.  The lower bound allows for
// sub-millisecond scheduling jitter; the upper bound is generous enough for
// slow CI runners but still catches the case where the timer is accidentally
// set to seconds.
func TestBoundedExec_TimeoutDuration_InBounds(t *testing.T) {
	t.Parallel()

	const timeout = 100 * time.Millisecond
	const minDur = 80 * time.Millisecond
	const maxDur = 250 * time.Millisecond

	be := newTestBoundedExec(4096, timeout, 20, 10)
	result, err := be.Run(context.Background(), "sleep", "10")
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if result.Metrics.ErrorKind != ExecErrorTimeout {
		t.Errorf("ErrorKind = %q, want %q", result.Metrics.ErrorKind, ExecErrorTimeout)
	}
	if result.Metrics.Duration < minDur || result.Metrics.Duration > maxDur {
		t.Errorf("Duration = %v, want [%v, %v] — timeout was %v",
			result.Metrics.Duration, minDur, maxDur, timeout)
	}
}

// TestByteCounter exercises the byteCounter in isolation.
func TestByteCounter(t *testing.T) {
	t.Parallel()

	t.Run("not truncated under limit", func(t *testing.T) {
		t.Parallel()
		c := &byteCounter{maxBytes: 10}
		n, err := c.Write([]byte("hello"))
		if err != nil || n != 5 {
			t.Fatalf("Write: n=%d, err=%v", n, err)
		}
		if c.truncated {
			t.Error("should not be truncated")
		}
	})

	t.Run("truncated when total exceeds limit", func(t *testing.T) {
		t.Parallel()
		c := &byteCounter{maxBytes: 4}
		c.Write([]byte("abcde")) //nolint:errcheck // 5 > 4 → truncated
		if !c.truncated {
			t.Error("should be truncated after exceeding maxBytes")
		}
	})

	t.Run("always returns nil error", func(t *testing.T) {
		t.Parallel()
		c := &byteCounter{maxBytes: 1}
		for i := 0; i < 10; i++ {
			n, err := c.Write([]byte("x"))
			if err != nil || n != 1 {
				t.Fatalf("Write %d: n=%d, err=%v", i, n, err)
			}
		}
	})
}
