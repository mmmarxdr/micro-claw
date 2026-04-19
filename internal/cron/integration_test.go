//go:build integration

package cron_test

import (
	"context"
	"os"
	"testing"
	"time"

	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/cron"
	"daimon/internal/store"
)

// TestCronIntegration_ScheduleAndFire creates a real SQLiteStore, registers
// a job with "@every 200ms" schedule, starts the scheduler, and verifies that
// an IncomingMessage is received on the inbox within 1s with the correct
// ChannelID and Prompt. It also checks last_run_at is updated in the DB.
func TestCronIntegration_ScheduleAndFire(t *testing.T) {
	// Create a temp dir for SQLite.
	tmpDir, err := os.MkdirTemp("", "cron-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Open SQLiteStore.
	st, err := store.NewSQLiteStore(config.StoreConfig{
		Type: "sqlite",
		Path: tmpDir,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Insert a job directly into the store.
	jobID := "integ-job-1"
	job := store.CronJob{
		ID:            jobID,
		Schedule:      "@every 200ms",
		ScheduleHuman: "every 200ms",
		Prompt:        "integration test prompt",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	createdJob, err := st.CreateJob(ctx, job)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Create and start scheduler.
	scheduler := cron.NewScheduler(st, time.UTC, 30, 50)
	inbox := make(chan channel.IncomingMessage, 10)

	if err := scheduler.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer scheduler.Stop()

	// Wait up to 1s for a message.
	select {
	case msg := <-inbox:
		if msg.ChannelID != "cron:"+jobID {
			t.Errorf("expected ChannelID %q, got %q", "cron:"+jobID, msg.ChannelID)
		}
		if msg.Text() != createdJob.Prompt {
			t.Errorf("expected Text() %q, got %q", createdJob.Prompt, msg.Text())
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for inbox message from scheduler")
	}

	scheduler.Stop()

	// Verify last_run_at was updated in DB.
	updatedJob, err := st.GetJob(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJob after fire: %v", err)
	}
	if updatedJob.LastRunAt == nil {
		t.Error("expected LastRunAt to be set after job fired")
	}
}
