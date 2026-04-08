package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCountingWriter(t *testing.T) {
	t.Run("writes within limit", func(t *testing.T) {
		var buf bytes.Buffer
		cw := &countingWriter{
			writer:       &buf,
			maxBytes:     100,
			bytesWritten: 0,
			truncated:    false,
		}

		n, err := cw.Write([]byte("hello world"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != 11 {
			t.Errorf("Write returned %d bytes, want 11", n)
		}
		if cw.bytesWritten != 11 {
			t.Errorf("bytesWritten = %d, want 11", cw.bytesWritten)
		}
		if cw.truncated {
			t.Error("truncated should be false")
		}
		if buf.String() != "hello world" {
			t.Errorf("buffer contains %q, want 'hello world'", buf.String())
		}
	})

	t.Run("returns ErrMaxBytes when limit hit", func(t *testing.T) {
		var buf bytes.Buffer
		cw := &countingWriter{
			writer:       &buf,
			maxBytes:     10,
			bytesWritten: 0,
			truncated:    false,
		}

		// First write should succeed
		n, err := cw.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("First Write failed: %v", err)
		}
		if n != 5 {
			t.Errorf("First Write returned %d bytes, want 5", n)
		}

		// Second write should hit limit
		n, err = cw.Write([]byte(" world"))
		if !errors.Is(err, ErrMaxBytes) {
			t.Errorf("Second Write error = %v, want ErrMaxBytes", err)
		}
		if n != 5 {
			t.Errorf("Second Write returned %d bytes, want 5 (up to remaining limit)", n)
		}
		if !cw.truncated {
			t.Error("truncated should be true after hitting limit")
		}
		if cw.bytesWritten != 10 {
			t.Errorf("bytesWritten = %d, want 10 (5 from first write + 5 from second up to limit)", cw.bytesWritten)
		}
		if buf.String() != "hello worl" {
			t.Errorf("buffer contains %q, want 'hello worl' (first 10 bytes)", buf.String())
		}
	})

	t.Run("exact boundary write", func(t *testing.T) {
		var buf bytes.Buffer
		cw := &countingWriter{
			writer:       &buf,
			maxBytes:     5,
			bytesWritten: 0,
			truncated:    false,
		}

		n, err := cw.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d bytes, want 5", n)
		}
		if cw.bytesWritten != 5 {
			t.Errorf("bytesWritten = %d, want 5", cw.bytesWritten)
		}
		if cw.truncated {
			t.Error("truncated should be false when writing exactly at limit")
		}
		if buf.String() != "hello" {
			t.Errorf("buffer contains %q, want 'hello'", buf.String())
		}
	})

	t.Run("write exceeds limit in single call", func(t *testing.T) {
		var buf bytes.Buffer
		cw := &countingWriter{
			writer:       &buf,
			maxBytes:     5,
			bytesWritten: 0,
			truncated:    false,
		}

		n, err := cw.Write([]byte("hello world"))
		if !errors.Is(err, ErrMaxBytes) {
			t.Errorf("Write error = %v, want ErrMaxBytes", err)
		}
		if n != 5 {
			t.Errorf("Write returned %d bytes, want 5 (up to limit)", n)
		}
		if !cw.truncated {
			t.Error("truncated should be true")
		}
		if cw.bytesWritten != 5 {
			t.Errorf("bytesWritten = %d, want 5 (bytes written up to limit)", cw.bytesWritten)
		}
		if buf.String() != "hello" {
			t.Errorf("buffer contains %q, want 'hello' (first 5 bytes)", buf.String())
		}
	})
}

func TestSandboxRun(t *testing.T) {
	t.Run("runs simple command", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		result, err := sb.Run(context.Background(), "echo", "hello")
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		if !strings.Contains(result.Summary, "hello") {
			t.Errorf("Summary = %q, should contain 'hello'", result.Summary)
		}
		if result.Metrics.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", result.Metrics.ExitCode)
		}
		if result.Metrics.StdoutBytes == 0 {
			t.Error("StdoutBytes should be non-zero")
		}
		if result.Metrics.Duration == 0 {
			t.Error("Duration should be non-zero")
		}
	})

	t.Run("captures stderr", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		// Use a command that writes to stderr
		result, err := sb.Run(context.Background(), "sh", "-c", "echo 'error' >&2")
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		if result.Metrics.StderrBytes == 0 {
			t.Error("StderrBytes should be non-zero for command that writes to stderr")
		}
		if result.Metrics.ExitCode != 0 {
			t.Errorf("ExitCode = %d, want 0", result.Metrics.ExitCode)
		}
	})

	t.Run("respects timeout", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        100 * time.Millisecond,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		// Sleep for longer than timeout
		result, err := sb.Run(context.Background(), "sleep", "1")
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
		// The command should be killed by context timeout
		// Duration should be roughly the timeout value
		if result.Metrics.Duration < 50*time.Millisecond || result.Metrics.Duration > 300*time.Millisecond {
			t.Errorf("Duration = %v, should be around timeout (100ms)", result.Metrics.Duration)
		}
	})

	t.Run("truncates large output", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 50, // Small limit
			Timeout:        5 * time.Second,
			KeepFirstN:     3,
			KeepLastN:      2,
		}

		// Generate output that exceeds limit - produce long lines
		result, err := sb.Run(context.Background(), "sh", "-c", "i=1; while [ $i -le 10 ]; do echo \"This is output line number $i which should be quite long enough to exceed the byte limit\"; i=$((i+1)); done")
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		t.Logf("Output length: %d, MaxOutputBytes: %d", len(result.Output), sb.MaxOutputBytes)
		t.Logf("Truncated: %v", result.Metrics.Truncated)
		t.Logf("Summary (first 200 chars): %.200s", result.Summary)

		// Check if output should have been truncated
		// Note: counting writers limit each stream individually, not combined
		// So total output might be > MaxOutputBytes even if neither stream hits limit individually
		// For this test, we're writing to stdout only, so it should truncate
		if len(result.Output) > sb.MaxOutputBytes && !result.Metrics.Truncated {
			t.Error("Metrics.Truncated should be true when output exceeds MaxOutputBytes")
		}
		// If truncated, summary should contain truncation notice
		if result.Metrics.Truncated && !strings.Contains(result.Summary, "[truncated:") {
			t.Errorf("Summary should contain truncation notice when truncated, got: %.200s", result.Summary)
		}
	})

	t.Run("handles command failure", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		result, err := sb.Run(context.Background(), "false")
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		if result.Metrics.ExitCode != 1 {
			t.Errorf("ExitCode = %d, want 1 for 'false' command", result.Metrics.ExitCode)
		}
	})
}

func TestSandboxSummaryExtraction(t *testing.T) {
	t.Run("returns full output when not truncated", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		output := "line1\nline2\nline3\n"
		metrics := SandboxMetrics{
			StdoutBytes: len(output),
			StderrBytes: 0,
			ExitCode:    0,
			Duration:    100 * time.Millisecond,
			Truncated:   false,
		}

		summary := sb.extractSummary(output, false, metrics)
		if summary != output {
			t.Errorf("Summary = %q, want %q", summary, output)
		}
	})

	t.Run("extracts summary for truncated output with many lines", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 100, // Small limit to force truncation
			Timeout:        5 * time.Second,
			KeepFirstN:     3,
			KeepLastN:      2,
		}

		// Create 10 lines of output
		var lines []string
		for i := 1; i <= 10; i++ {
			lines = append(lines, fmt.Sprintf("line%d", i))
		}
		output := strings.Join(lines, "\n") + "\n"

		metrics := SandboxMetrics{
			StdoutBytes: len(output),
			StderrBytes: 0,
			ExitCode:    0,
			Duration:    123 * time.Millisecond,
			Truncated:   true,
		}

		summary := sb.extractSummary(output, true, metrics)

		// Expected: first 3 lines + truncation notice + last 2 lines
		expectedHead := "line1\nline2\nline3"
		expectedTail := "line9\nline10"
		// The truncation notice format from sandbox.go
		expectedNotice := fmt.Sprintf("\n...\n[truncated: %d/%d bytes, %v]\n...\n",
			len(output), sb.MaxOutputBytes, metrics.Duration.Round(time.Millisecond))
		expectedSummary := expectedHead + expectedNotice + expectedTail

		if summary != expectedSummary {
			t.Errorf("Summary mismatch\nGot: %q\nWant: %q", summary, expectedSummary)
		}
	})

	t.Run("handles empty output", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		output := ""
		metrics := SandboxMetrics{
			StdoutBytes: 0,
			StderrBytes: 0,
			ExitCode:    0,
			Duration:    100 * time.Millisecond,
			Truncated:   false,
		}

		summary := sb.extractSummary(output, false, metrics)
		if summary != "" {
			t.Errorf("Summary = %q, want empty string", summary)
		}
	})

	t.Run("shows truncation notice for short truncated output", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 100,
			Timeout:        5 * time.Second,
			KeepFirstN:     3,
			KeepLastN:      2,
		}

		// Only 4 lines total (less than 3+2=5) but truncated
		output := "line1\nline2\nline3\nline4\n"
		metrics := SandboxMetrics{
			StdoutBytes: len(output),
			StderrBytes: 0,
			ExitCode:    0,
			Duration:    100 * time.Millisecond,
			Truncated:   true,
		}

		summary := sb.extractSummary(output, true, metrics)
		// Should show full output + truncation notice
		expectedNotice := fmt.Sprintf("\n...\n[truncated: %d/%d bytes, %v]\n...\n",
			len(output), sb.MaxOutputBytes, metrics.Duration.Round(time.Millisecond))
		expected := output + expectedNotice
		if summary != expected {
			t.Errorf("Summary = %q, want %q", summary, expected)
		}
	})

	t.Run("handles output without trailing newline", func(t *testing.T) {
		sb := &Sandbox{
			MaxOutputBytes: 4096,
			Timeout:        5 * time.Second,
			KeepFirstN:     20,
			KeepLastN:      10,
		}

		output := "line1\nline2\nline3" // No trailing newline
		metrics := SandboxMetrics{
			StdoutBytes: len(output),
			StderrBytes: 0,
			ExitCode:    0,
			Duration:    100 * time.Millisecond,
			Truncated:   false,
		}

		summary := sb.extractSummary(output, false, metrics)
		if summary != output {
			t.Errorf("Summary = %q, want %q", summary, output)
		}
	})
}
