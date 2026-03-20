package cron

import (
	"context"
	"errors"
	"testing"
	"time"

	"microagent/internal/channel"
	"microagent/internal/store"
)

// ─── in-memory stub CronStore ─────────────────────────────────────────────────

type stubCronStore struct {
	jobs    map[string]store.CronJob
	results []store.CronResult
}

func newStubStore() *stubCronStore {
	return &stubCronStore{jobs: make(map[string]store.CronJob)}
}

func (s *stubCronStore) CreateJob(_ context.Context, job store.CronJob) (store.CronJob, error) {
	s.jobs[job.ID] = job
	return job, nil
}

func (s *stubCronStore) ListJobs(_ context.Context) ([]store.CronJob, error) {
	out := make([]store.CronJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		if j.Enabled {
			out = append(out, j)
		}
	}
	return out, nil
}

func (s *stubCronStore) GetJob(_ context.Context, id string) (store.CronJob, error) {
	j, ok := s.jobs[id]
	if !ok {
		return store.CronJob{}, store.ErrNotFound
	}
	return j, nil
}

func (s *stubCronStore) DeleteJob(_ context.Context, id string) error {
	if _, ok := s.jobs[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.jobs, id)
	return nil
}

func (s *stubCronStore) SaveResult(_ context.Context, r store.CronResult) error {
	s.results = append(s.results, r)
	return nil
}

func (s *stubCronStore) ListResults(_ context.Context, _ string, _ int) ([]store.CronResult, error) {
	return s.results, nil
}

func (s *stubCronStore) PruneResults(_ context.Context, _, _ int) error { return nil }

func (s *stubCronStore) UpdateJobRunTimes(_ context.Context, id string, _, _ time.Time) error {
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func makeJob(id, schedule string) store.CronJob {
	return store.CronJob{
		ID:            id,
		Schedule:      schedule,
		ScheduleHuman: "test schedule",
		Prompt:        "test prompt",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
}

func makeInbox() chan channel.IncomingMessage {
	return make(chan channel.IncomingMessage, 10)
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestScheduler_LoadsJobsOnStart verifies that after Start, entryIDs has an
// entry for each enabled job in the store.
func TestScheduler_LoadsJobsOnStart(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job1 := makeJob("job-1", "0 9 * * *")
	job2 := makeJob("job-2", "0 18 * * *")
	st.jobs["job-1"] = job1
	st.jobs["job-2"] = job2

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()

	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	sched.mu.Lock()
	n := len(sched.entryIDs)
	sched.mu.Unlock()

	if n != 2 {
		t.Errorf("expected 2 entryIDs after Start, got %d", n)
	}
	for _, id := range []string{"job-1", "job-2"} {
		sched.mu.Lock()
		_, ok := sched.entryIDs[id]
		sched.mu.Unlock()
		if !ok {
			t.Errorf("expected entryIDs to contain %q", id)
		}
	}
}

// TestScheduler_AddJob verifies that AddJob registers the job in entryIDs.
func TestScheduler_AddJob(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()

	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	job := makeJob("new-job", "0 12 * * *")
	if err := sched.AddJob(ctx, job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	sched.mu.Lock()
	_, ok := sched.entryIDs["new-job"]
	sched.mu.Unlock()

	if !ok {
		t.Error("expected entryIDs to contain 'new-job' after AddJob")
	}
}

// TestScheduler_RemoveJob verifies that RemoveJob removes the entry from entryIDs.
func TestScheduler_RemoveJob(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()

	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	job := makeJob("rm-job", "0 6 * * *")
	if err := sched.AddJob(ctx, job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	if err := sched.RemoveJob(ctx, "rm-job"); err != nil {
		t.Fatalf("RemoveJob: %v", err)
	}

	sched.mu.Lock()
	_, ok := sched.entryIDs["rm-job"]
	sched.mu.Unlock()

	if ok {
		t.Error("expected entryIDs NOT to contain 'rm-job' after RemoveJob")
	}
}

// TestScheduler_RemoveJob_NotFound verifies ErrJobNotFound for unknown IDs.
func TestScheduler_RemoveJob_NotFound(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()

	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	err := sched.RemoveJob(ctx, "nonexistent-id")
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

// TestScheduler_Stop_NoRace verifies Start → Stop completes without a race.
// Run with: go test -race ./internal/cron/...
func TestScheduler_Stop_NoRace(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()

	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Add a job to ensure there's something registered.
	job := makeJob("race-job", "0 0 * * *")
	if err := sched.AddJob(ctx, job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	sched.Stop() // Must not race.
}

// TestScheduler_FireJob_SendsToInbox verifies that fireJob pushes an
// IncomingMessage into the inbox. Calls fireJob directly (same package)
// to avoid flaky timing dependencies on robfig/cron's tick interval.
func TestScheduler_FireJob_SendsToInbox(t *testing.T) {
	st := newStubStore()
	inbox := make(chan channel.IncomingMessage, 10)

	sched := NewScheduler(st, time.UTC, 30, 50)
	sched.inbox = inbox // set directly — same package access

	job := store.CronJob{
		ID:        "fire-job-1",
		Schedule:  "0 9 * * *",
		Prompt:    "fire test prompt",
		ChannelID: "cli",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}

	sched.fireJob(job)

	select {
	case msg := <-inbox:
		if msg.ChannelID != "cron:"+job.ID {
			t.Errorf("expected ChannelID %q, got %q", "cron:"+job.ID, msg.ChannelID)
		}
		if msg.Text != job.Prompt {
			t.Errorf("expected Text %q, got %q", job.Prompt, msg.Text)
		}
		if msg.SenderID != "cron" {
			t.Errorf("expected SenderID %q, got %q", "cron", msg.SenderID)
		}
	default:
		t.Fatal("inbox empty after fireJob — message was not sent")
	}
}
