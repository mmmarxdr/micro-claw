package cron

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	robfigcron "github.com/robfig/cron/v3"

	"microagent/internal/channel"
	"microagent/internal/content"
	"microagent/internal/store"
)

// ErrJobNotFound is returned when an operation targets a job ID not in the scheduler.
var ErrJobNotFound = errors.New("cron: job not found in scheduler")

// SchedulerIface is the subset of Scheduler used by CronChannel and the cron tools.
// It allows testing without a real cron process.
type SchedulerIface interface {
	Start(ctx context.Context, inbox chan<- channel.IncomingMessage) error
	Stop()
	AddJob(ctx context.Context, job store.CronJob) error
	RemoveJob(ctx context.Context, jobID string) error
}

// Scheduler wraps robfig/cron/v3 and bridges cron job fires to the agent inbox.
type Scheduler struct {
	cron          *robfigcron.Cron
	cronStore     store.CronStore
	inbox         chan<- channel.IncomingMessage
	tz            *time.Location
	entryIDs      map[string]robfigcron.EntryID
	mu            sync.Mutex
	retentionDays int
	maxPerJob     int
}

// NewScheduler constructs a Scheduler. If tz is nil, time.UTC is used.
func NewScheduler(cronStore store.CronStore, tz *time.Location, retentionDays, maxPerJob int) *Scheduler {
	if tz == nil {
		tz = time.UTC
	}
	return &Scheduler{
		cronStore:     cronStore,
		tz:            tz,
		entryIDs:      make(map[string]robfigcron.EntryID),
		retentionDays: retentionDays,
		maxPerJob:     maxPerJob,
	}
}

// Start saves the inbox reference, loads all enabled jobs from the store, registers
// each with robfig/cron, starts the cron loop, and runs an initial prune.
// Non-blocking: the robfig scheduler runs in its own goroutines.
func (s *Scheduler) Start(ctx context.Context, inbox chan<- channel.IncomingMessage) error {
	s.inbox = inbox
	s.cron = robfigcron.New(robfigcron.WithLocation(s.tz))

	jobs, err := s.cronStore.ListJobs(ctx)
	if err != nil {
		return err
	}

	for _, job := range jobs {
		if err := s.registerJob(job); err != nil {
			slog.Warn("cron: failed to register job on start",
				"job_id", job.ID,
				"schedule", job.Schedule,
				"err", err,
			)
		}
	}

	s.cron.Start()

	// Initial prune (best-effort).
	s.pruneResults(ctx)

	return nil
}

// Stop halts the scheduler and blocks until all running job goroutines finish.
func (s *Scheduler) Stop() {
	if s.cron == nil {
		return
	}
	stopCtx := s.cron.Stop()
	<-stopCtx.Done()
}

// AddJob registers a new job with the running scheduler dynamically.
func (s *Scheduler) AddJob(ctx context.Context, job store.CronJob) error {
	if err := s.registerJob(job); err != nil {
		return err
	}
	return nil
}

// RemoveJob unregisters a job from the running scheduler.
// Returns ErrJobNotFound if the job ID is not tracked.
func (s *Scheduler) RemoveJob(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entryID, ok := s.entryIDs[jobID]
	if !ok {
		return ErrJobNotFound
	}
	s.cron.Remove(entryID)
	delete(s.entryIDs, jobID)
	return nil
}

// registerJob adds a single job to the robfig cron instance and tracks its entry ID.
func (s *Scheduler) registerJob(job store.CronJob) error {
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.fireJob(job)
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.entryIDs[job.ID] = entryID
	s.mu.Unlock()

	return nil
}

// fireJob is called by robfig for each scheduled tick.
// It pushes an IncomingMessage into the inbox and updates run times.
func (s *Scheduler) fireJob(job store.CronJob) {
	if s.inbox == nil {
		slog.Warn("cron: fireJob called but inbox is nil", "job_id", job.ID)
		return
	}

	msg := channel.IncomingMessage{
		ChannelID: "cron:" + job.ID,
		SenderID:  "cron",
		Content:   content.TextBlock(job.Prompt),
		Timestamp: time.Now(),
	}

	// Non-blocking send: drop if inbox is full (prevents scheduler goroutine from blocking).
	select {
	case s.inbox <- msg:
	default:
		slog.Warn("cron: inbox full, dropping job fire", "job_id", job.ID)
	}

	// Update run times (best-effort).
	ctx := context.Background()
	now := time.Now()

	// Compute next run from schedule.
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	sched, err := parser.Parse(job.Schedule)
	var nextRun time.Time
	if err != nil {
		slog.Warn("cron: could not parse schedule for next run time", "job_id", job.ID, "err", err)
		nextRun = now.Add(24 * time.Hour) // fallback
	} else {
		nextRun = sched.Next(now)
	}

	if err := s.cronStore.UpdateJobRunTimes(ctx, job.ID, now, nextRun); err != nil {
		slog.Warn("cron: failed to update run times", "job_id", job.ID, "err", err)
	}

	s.pruneResults(ctx)
}

// pruneResults calls PruneResults on the store (best-effort, logs error).
func (s *Scheduler) pruneResults(ctx context.Context) {
	if err := s.cronStore.PruneResults(ctx, s.retentionDays, s.maxPerJob); err != nil {
		slog.Warn("cron: prune results failed", "err", err)
	}
}
