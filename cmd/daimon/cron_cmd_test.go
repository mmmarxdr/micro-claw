package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/store"
)

// cronTestConfig returns a minimal config YAML with sqlite store pointing to dir.
func cronTestConfig(dir string) string {
	return fmt.Sprintf(`
provider:
  type: anthropic
  model: claude-3-sonnet-20240229
  api_key: test-key
store:
  type: sqlite
  path: %s
cron:
  enabled: true
`, dir)
}

// writeCronConfig writes a cron-enabled config to a temp file and returns the path.
func writeCronConfig(t *testing.T) (cfgPath string, storeDir string) {
	t.Helper()
	storeDir = t.TempDir()
	cfgDir := t.TempDir()
	cfgPath = filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cronTestConfig(storeDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, storeDir
}

// openTestCronStore opens a SQLiteStore for the given directory.
func openTestCronStore(t *testing.T, dir string) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(config.StoreConfig{Type: "sqlite", Path: dir})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// captureStdout captures os.Stdout during fn() and returns the captured string.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// ─── TestCronListCmd_Empty ────────────────────────────────────────────────────

func TestCronListCmd_Empty(t *testing.T) {
	cfgPath, _ := writeCronConfig(t)

	output := captureStdout(t, func() {
		if err := cronList([]string{}, cfgPath); err != nil {
			t.Fatalf("cronList: %v", err)
		}
	})

	if !strings.Contains(output, "No cron jobs scheduled.") {
		t.Errorf("expected 'No cron jobs scheduled.' in output, got:\n%s", output)
	}
}

// ─── TestCronListCmd_WithJobs ─────────────────────────────────────────────────

func TestCronListCmd_WithJobs(t *testing.T) {
	cfgPath, storeDir := writeCronConfig(t)
	s := openTestCronStore(t, storeDir)
	ctx := context.Background()

	job1 := store.CronJob{
		ID:            "job-aaa111bbb222",
		Schedule:      "0 9 * * *",
		ScheduleHuman: "every day at 9:00 AM",
		Prompt:        "summarize the news",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	job2 := store.CronJob{
		ID:            "job-ccc333ddd444",
		Schedule:      "0 0 * * 1",
		ScheduleHuman: "every Monday at midnight",
		Prompt:        "clean up temp files",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}

	if _, err := s.CreateJob(ctx, job1); err != nil {
		t.Fatalf("CreateJob job1: %v", err)
	}
	if _, err := s.CreateJob(ctx, job2); err != nil {
		t.Fatalf("CreateJob job2: %v", err)
	}

	output := captureStdout(t, func() {
		if err := cronList([]string{}, cfgPath); err != nil {
			t.Fatalf("cronList: %v", err)
		}
	})

	// IDs are truncated to 12 chars.
	if !strings.Contains(output, "job-aaa111bb") {
		t.Errorf("expected job1 ID prefix in output, got:\n%s", output)
	}
	if !strings.Contains(output, "job-ccc333dd") {
		t.Errorf("expected job2 ID prefix in output, got:\n%s", output)
	}
}

// ─── TestCronDeleteCmd_Found ──────────────────────────────────────────────────

func TestCronDeleteCmd_Found(t *testing.T) {
	cfgPath, storeDir := writeCronConfig(t)
	s := openTestCronStore(t, storeDir)
	ctx := context.Background()

	job := store.CronJob{
		ID:            "delete-me-job-1",
		Schedule:      "0 9 * * *",
		ScheduleHuman: "every day at 9:00 AM",
		Prompt:        "do something",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	output := captureStdout(t, func() {
		if err := cronDelete([]string{"--yes", job.ID}, cfgPath); err != nil {
			t.Fatalf("cronDelete: %v", err)
		}
	})

	if !strings.Contains(output, "Deleted cron job") {
		t.Errorf("expected deletion confirmation, got:\n%s", output)
	}

	// Verify job is gone from store.
	_, err := s.GetJob(ctx, job.ID)
	if err == nil {
		t.Error("expected error after deletion, got nil")
	}
}

// ─── TestCronDeleteCmd_NotFound ───────────────────────────────────────────────

func TestCronDeleteCmd_NotFound(t *testing.T) {
	cfgPath, _ := writeCronConfig(t)

	err := cronDelete([]string{"--yes", "nonexistent-id-xyz"}, cfgPath)
	if err == nil {
		t.Error("expected error for nonexistent job, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// ─── TestCronInfoCmd_WithResults ──────────────────────────────────────────────

func TestCronInfoCmd_WithResults(t *testing.T) {
	cfgPath, storeDir := writeCronConfig(t)
	s := openTestCronStore(t, storeDir)
	ctx := context.Background()

	job := store.CronJob{
		ID:            "info-job-test-id",
		Schedule:      "0 10 * * *",
		ScheduleHuman: "every day at 10:00 AM",
		Prompt:        "fetch weather report",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	result1 := store.CronResult{
		ID:     "result-1",
		JobID:  job.ID,
		RanAt:  time.Now().UTC().Add(-2 * time.Hour),
		Output: "Weather: sunny, 25C",
	}
	result2 := store.CronResult{
		ID:       "result-2",
		JobID:    job.ID,
		RanAt:    time.Now().UTC().Add(-1 * time.Hour),
		ErrorMsg: "connection timeout",
	}
	if err := s.SaveResult(ctx, result1); err != nil {
		t.Fatalf("SaveResult 1: %v", err)
	}
	if err := s.SaveResult(ctx, result2); err != nil {
		t.Fatalf("SaveResult 2: %v", err)
	}

	output := captureStdout(t, func() {
		if err := cronInfo([]string{job.ID}, cfgPath); err != nil {
			t.Fatalf("cronInfo: %v", err)
		}
	})

	if !strings.Contains(output, job.ID) {
		t.Errorf("expected job ID in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Weather: sunny") {
		t.Errorf("expected result output in info, got:\n%s", output)
	}
	if !strings.Contains(output, "connection timeout") {
		t.Errorf("expected error message in info, got:\n%s", output)
	}
}
