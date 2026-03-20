package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCronStore_CreateAndGet(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	job := CronJob{
		ID:            "job-1",
		Schedule:      "0 9 * * *",
		ScheduleHuman: "every day at 9am",
		Prompt:        "summarize the news",
		ChannelID:     "telegram",
		Enabled:       true,
		CreatedAt:     now,
	}

	created, err := s.CreateJob(ctx, job)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if created.ID != job.ID {
		t.Errorf("CreateJob returned ID %q, want %q", created.ID, job.ID)
	}

	got, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != job.ID {
		t.Errorf("ID mismatch: got %q want %q", got.ID, job.ID)
	}
	if got.Schedule != job.Schedule {
		t.Errorf("Schedule mismatch: got %q want %q", got.Schedule, job.Schedule)
	}
	if got.ScheduleHuman != job.ScheduleHuman {
		t.Errorf("ScheduleHuman mismatch: got %q want %q", got.ScheduleHuman, job.ScheduleHuman)
	}
	if got.Prompt != job.Prompt {
		t.Errorf("Prompt mismatch: got %q want %q", got.Prompt, job.Prompt)
	}
	if got.ChannelID != job.ChannelID {
		t.Errorf("ChannelID mismatch: got %q want %q", got.ChannelID, job.ChannelID)
	}
	if !got.Enabled {
		t.Error("expected Enabled=true")
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt mismatch: got %v want %v", got.CreatedAt, now)
	}
	if got.LastRunAt != nil {
		t.Errorf("expected LastRunAt=nil, got %v", got.LastRunAt)
	}
	if got.NextRunAt != nil {
		t.Errorf("expected NextRunAt=nil, got %v", got.NextRunAt)
	}
}

func TestCronStore_ListJobs(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	// 2 enabled + 1 disabled
	for i := 0; i < 3; i++ {
		enabled := i < 2
		job := CronJob{
			ID:            fmt.Sprintf("job-%d", i),
			Schedule:      "0 * * * *",
			ScheduleHuman: "hourly",
			Prompt:        fmt.Sprintf("task %d", i),
			ChannelID:     "cli",
			Enabled:       enabled,
			CreatedAt:     now.Add(time.Duration(i) * time.Second),
		}
		if _, err := s.CreateJob(ctx, job); err != nil {
			t.Fatalf("CreateJob %d: %v", i, err)
		}
	}

	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 enabled jobs, got %d", len(jobs))
	}
	for _, j := range jobs {
		if !j.Enabled {
			t.Errorf("ListJobs returned disabled job %q", j.ID)
		}
	}
}

func TestCronStore_DeleteJob_Found(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	job := CronJob{
		ID:            "del-job-1",
		Schedule:      "0 0 * * *",
		ScheduleHuman: "daily",
		Prompt:        "clean up",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := s.DeleteJob(ctx, job.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	_, err := s.GetJob(ctx, job.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestCronStore_DeleteJob_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.DeleteJob(ctx, "nonexistent-job")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing job, got %v", err)
	}
}

func TestCronStore_SaveAndListResults(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	job := CronJob{
		ID:            "results-job",
		Schedule:      "* * * * *",
		ScheduleHuman: "every minute",
		Prompt:        "ping",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		r := CronResult{
			ID:     fmt.Sprintf("res-%d", i),
			JobID:  job.ID,
			RanAt:  base.Add(time.Duration(i) * time.Minute),
			Output: fmt.Sprintf("output %d", i),
		}
		if err := s.SaveResult(ctx, r); err != nil {
			t.Fatalf("SaveResult %d: %v", i, err)
		}
	}

	// limit=2 should return only the 2 newest
	results, err := s.ListResults(ctx, job.ID, 2)
	if err != nil {
		t.Fatalf("ListResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (limit=2), got %d", len(results))
	}
	// newest first: res-2 then res-1
	if results[0].ID != "res-2" {
		t.Errorf("expected newest result first (res-2), got %q", results[0].ID)
	}
	if results[1].ID != "res-1" {
		t.Errorf("expected second result (res-1), got %q", results[1].ID)
	}
}

func TestCronStore_PruneResults_Retention(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	job := CronJob{
		ID:            "prune-retention-job",
		Schedule:      "* * * * *",
		ScheduleHuman: "every minute",
		Prompt:        "ping",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	now := time.Now().UTC()
	// 2 old results (35 days ago), 1 recent result
	for i := 0; i < 2; i++ {
		r := CronResult{
			ID:    fmt.Sprintf("old-%d", i),
			JobID: job.ID,
			RanAt: now.AddDate(0, 0, -35),
		}
		if err := s.SaveResult(ctx, r); err != nil {
			t.Fatalf("SaveResult old %d: %v", i, err)
		}
	}
	recent := CronResult{
		ID:    "recent-1",
		JobID: job.ID,
		RanAt: now,
	}
	if err := s.SaveResult(ctx, recent); err != nil {
		t.Fatalf("SaveResult recent: %v", err)
	}

	// Prune with 30-day retention, no maxPerJob limit
	if err := s.PruneResults(ctx, 30, 0); err != nil {
		t.Fatalf("PruneResults: %v", err)
	}

	results, err := s.ListResults(ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ListResults after prune: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result after retention prune, got %d", len(results))
	}
	if len(results) == 1 && results[0].ID != "recent-1" {
		t.Errorf("expected recent-1 to survive prune, got %q", results[0].ID)
	}
}

func TestCronStore_PruneResults_MaxPerJob(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	job := CronJob{
		ID:            "prune-max-job",
		Schedule:      "* * * * *",
		ScheduleHuman: "every minute",
		Prompt:        "ping",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	if _, err := s.CreateJob(ctx, job); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Second)
	// Insert 10 results, newest = index 9
	for i := 0; i < 10; i++ {
		r := CronResult{
			ID:    fmt.Sprintf("r-%02d", i),
			JobID: job.ID,
			RanAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := s.SaveResult(ctx, r); err != nil {
			t.Fatalf("SaveResult %d: %v", i, err)
		}
	}

	// maxPerJob=5 should keep only the 5 newest (r-05..r-09)
	if err := s.PruneResults(ctx, 0, 5); err != nil {
		t.Fatalf("PruneResults: %v", err)
	}

	results, err := s.ListResults(ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("ListResults after prune: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 results after maxPerJob prune, got %d", len(results))
	}
	// Results ordered newest first: r-09, r-08, r-07, r-06, r-05
	if len(results) == 5 && results[0].ID != "r-09" {
		t.Errorf("expected newest result r-09 first, got %q", results[0].ID)
	}
}

func TestCronStore_PruneResults_Atomicity(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Empty tables — pruning should succeed without error
	if err := s.PruneResults(ctx, 30, 50); err != nil {
		t.Errorf("PruneResults on empty tables returned error: %v", err)
	}
}
