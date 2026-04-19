package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"daimon/internal/channel"
	"daimon/internal/cron"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Mock scheduler for cron command tests
// ---------------------------------------------------------------------------

type mockScheduler struct {
	activeJobs    []cron.ActiveJob
	activeJobsErr error
	addJobErr     error
	removeJobErr  error
	addedJobs     []store.CronJob
	removedIDs    []string
}

func (m *mockScheduler) Start(_ context.Context, _ chan<- channel.IncomingMessage) error {
	return nil
}

func (m *mockScheduler) Stop() {}

func (m *mockScheduler) AddJob(_ context.Context, job store.CronJob) error {
	if m.addJobErr != nil {
		return m.addJobErr
	}
	m.addedJobs = append(m.addedJobs, job)
	return nil
}

func (m *mockScheduler) RemoveJob(_ context.Context, jobID string) error {
	if m.removeJobErr != nil {
		return m.removeJobErr
	}
	m.removedIDs = append(m.removedIDs, jobID)
	return nil
}

func (m *mockScheduler) ListActiveJobs(_ context.Context) ([]cron.ActiveJob, error) {
	return m.activeJobs, m.activeJobsErr
}

// ---------------------------------------------------------------------------
// Mock CronStore for cron command tests
// ---------------------------------------------------------------------------

type mockCronStoreAgent struct {
	jobs       []store.CronJob
	results    []store.CronResult
	deleteErr  error
	createErr  error
	listErr    error
	deletedIDs []string
	createdJob *store.CronJob
}

func (m *mockCronStoreAgent) CreateJob(_ context.Context, job store.CronJob) (store.CronJob, error) {
	if m.createErr != nil {
		return store.CronJob{}, m.createErr
	}
	m.createdJob = &job
	return job, nil
}

func (m *mockCronStoreAgent) ListJobs(_ context.Context) ([]store.CronJob, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.jobs, nil
}

func (m *mockCronStoreAgent) GetJob(_ context.Context, id string) (store.CronJob, error) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return store.CronJob{}, store.ErrNotFound
}

func (m *mockCronStoreAgent) DeleteJob(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedIDs = append(m.deletedIDs, id)
	return nil
}

func (m *mockCronStoreAgent) SaveResult(_ context.Context, r store.CronResult) error {
	m.results = append(m.results, r)
	return nil
}

func (m *mockCronStoreAgent) ListResults(_ context.Context, jobID string, limit int) ([]store.CronResult, error) {
	var out []store.CronResult
	for _, r := range m.results {
		if r.JobID == jobID {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *mockCronStoreAgent) PruneResults(_ context.Context, _, _ int) error { return nil }

func (m *mockCronStoreAgent) CountResults(_ context.Context, _ string) (int, error) { return 0, nil }

func (m *mockCronStoreAgent) UpdateJobRunTimes(_ context.Context, _ string, _, _ time.Time) error {
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeCronCC builds a CommandContext wired to the given reply capture and stores.
func makeCronCC(cr *capturedReply, cronSt store.CronStore, sched cron.SchedulerIface) CommandContext {
	return CommandContext{
		Ctx:       context.Background(),
		ChannelID: "cli",
		SenderID:  "user:1",
		Reply:     cr.reply,
		Store:     &mockStore{},
	}
}

// makeTestJob builds a fake CronJob with a specific UUID and schedule.
func makeTestJob(id, schedule, prompt string) store.CronJob {
	return store.CronJob{
		ID:          id,
		Schedule:    schedule,
		Prompt:      prompt,
		ChannelID:   "cli",
		Description: prompt,
		Enabled:     true,
		CreatedAt:   time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// T4.1 — resolveJobID tests
// ---------------------------------------------------------------------------

func TestResolveJobID(t *testing.T) {
	job1 := makeTestJob("aaaabbbb-cccc-dddd-eeee-ffffffffffff", "0 9 * * *", "morning briefing")
	job2 := makeTestJob("bbbbcccc-dddd-eeee-ffff-aaaaaaaaaaaa", "0 18 * * *", "evening summary")
	// job3 has the same 8-char prefix as job1 intentionally for ambiguity test
	job3 := makeTestJob("aaaabbbb-xxxx-xxxx-xxxx-xxxxxxxxxxxx", "0 12 * * *", "noon check")

	tests := []struct {
		name      string
		jobs      []store.CronJob
		prefix    string
		wantID    string
		wantError string
	}{
		{
			name:   "exact match full UUID",
			jobs:   []store.CronJob{job1, job2},
			prefix: "aaaabbbb-cccc-dddd-eeee-ffffffffffff",
			wantID: job1.ID,
		},
		{
			name:   "prefix match 8 chars",
			jobs:   []store.CronJob{job1, job2},
			prefix: "bbbbcccc",
			wantID: job2.ID,
		},
		{
			name:      "ambiguous prefix",
			jobs:      []store.CronJob{job1, job3},
			prefix:    "aaaabbbb",
			wantError: "ambiguous",
		},
		{
			name:      "not found",
			jobs:      []store.CronJob{job1, job2},
			prefix:    "zzzzzzz1",
			wantError: "no task found",
		},
		{
			name:      "empty list",
			jobs:      []store.CronJob{},
			prefix:    "aaaabbbb",
			wantError: "no task found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveJobID(tc.jobs, tc.prefix)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantError)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Errorf("expected error %q, got %q", tc.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != tc.wantID {
				t.Errorf("expected ID=%q, got %q", tc.wantID, got.ID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// T4.2 — /tasks tests
// ---------------------------------------------------------------------------

func TestCmdTasks_WithJobs(t *testing.T) {
	job1 := makeTestJob("aaaaaaaa-0000-0000-0000-000000000001", "0 9 * * *", "morning briefing")
	job2 := makeTestJob("bbbbbbbb-0000-0000-0000-000000000002", "0 18 * * *", "evening summary")

	sched := &mockScheduler{
		activeJobs: []cron.ActiveJob{
			{Job: job1, NextRun: time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC)},
			{Job: job2, NextRun: time.Date(2026, 4, 12, 18, 0, 0, 0, time.UTC)},
		},
	}

	cr := &capturedReply{}
	cc := makeCronCC(cr, nil, sched)

	handler := makeCmdTasks(sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]

	// Should contain short IDs (first 8 chars)
	if !strings.Contains(reply, "aaaaaaaa") {
		t.Errorf("expected short ID 'aaaaaaaa' in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "bbbbbbbb") {
		t.Errorf("expected short ID 'bbbbbbbb' in reply, got:\n%s", reply)
	}
	// Should contain schedules
	if !strings.Contains(reply, "0 9 * * *") {
		t.Errorf("expected schedule '0 9 * * *' in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "0 18 * * *") {
		t.Errorf("expected schedule '0 18 * * *' in reply, got:\n%s", reply)
	}
}

func TestCmdTasks_Empty(t *testing.T) {
	sched := &mockScheduler{activeJobs: []cron.ActiveJob{}}

	cr := &capturedReply{}
	cc := makeCronCC(cr, nil, sched)

	handler := makeCmdTasks(sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "No scheduled tasks") {
		t.Errorf("expected 'No scheduled tasks.' reply, got %q", cr.messages[0])
	}
}

// ---------------------------------------------------------------------------
// T4.3 — /cancel and /cancel-confirm tests
// ---------------------------------------------------------------------------

func TestCmdCancel_ValidID(t *testing.T) {
	job := makeTestJob("cccccccc-0000-0000-0000-000000000003", "0 9 * * *", "test morning task")
	cronSt := &mockCronStoreAgent{jobs: []store.CronJob{job}}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = "cccccccc" // short-ID prefix

	handler := makeCmdCancel(cronSt, &mockScheduler{})
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]
	// Should contain job details + confirmation prompt
	if !strings.Contains(reply, "cccccccc") {
		t.Errorf("expected short ID 'cccccccc' in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "0 9 * * *") {
		t.Errorf("expected schedule in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "/cancel-confirm") {
		t.Errorf("expected '/cancel-confirm' prompt in reply, got:\n%s", reply)
	}
}

func TestCmdCancel_NoArgs(t *testing.T) {
	cronSt := &mockCronStoreAgent{}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = ""

	handler := makeCmdCancel(cronSt, &mockScheduler{})
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "Usage: /cancel") {
		t.Errorf("expected usage message, got %q", cr.messages[0])
	}
}

func TestCmdCancel_InvalidID(t *testing.T) {
	job := makeTestJob("dddddddd-0000-0000-0000-000000000004", "0 9 * * *", "some task")
	cronSt := &mockCronStoreAgent{jobs: []store.CronJob{job}}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = "eeeeeeee" // non-existent prefix

	handler := makeCmdCancel(cronSt, &mockScheduler{})
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "no task found") {
		t.Errorf("expected 'no task found' message, got %q", cr.messages[0])
	}
}

func TestCmdCancelConfirm_ValidID(t *testing.T) {
	job := makeTestJob("ffffffff-0000-0000-0000-000000000005", "0 9 * * *", "confirm cancel task")
	cronSt := &mockCronStoreAgent{jobs: []store.CronJob{job}}
	sched := &mockScheduler{}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, sched)
	cc.Args = "ffffffff" // short ID prefix

	handler := makeCmdCancelConfirm(cronSt, sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "cancelled") {
		t.Errorf("expected 'cancelled' in reply, got %q", cr.messages[0])
	}
	// Verify DeleteJob was called
	if len(cronSt.deletedIDs) != 1 {
		t.Errorf("expected DeleteJob to be called once, got %d calls", len(cronSt.deletedIDs))
	}
	if cronSt.deletedIDs[0] != job.ID {
		t.Errorf("expected DeleteJob called with %q, got %q", job.ID, cronSt.deletedIDs[0])
	}
	// Verify RemoveJob was called on scheduler
	if len(sched.removedIDs) != 1 {
		t.Errorf("expected RemoveJob to be called once, got %d calls", len(sched.removedIDs))
	}
}

// ---------------------------------------------------------------------------
// T4.4 — /schedule tests
// ---------------------------------------------------------------------------

func TestCmdSchedule_Valid(t *testing.T) {
	cronSt := &mockCronStoreAgent{}
	sched := &mockScheduler{}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, sched)
	cc.Args = "0 9 * * * Good morning summary"

	handler := makeCmdSchedule(cronSt, sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	// Verify reply contains "Scheduled"
	if !strings.Contains(cr.messages[0], "Scheduled") {
		t.Errorf("expected 'Scheduled' in reply, got %q", cr.messages[0])
	}
	// Verify CreateJob was called
	if cronSt.createdJob == nil {
		t.Error("expected CreateJob to be called")
	} else {
		if cronSt.createdJob.Schedule != "0 9 * * *" {
			t.Errorf("expected schedule '0 9 * * *', got %q", cronSt.createdJob.Schedule)
		}
		if cronSt.createdJob.Prompt != "Good morning summary" {
			t.Errorf("expected prompt 'Good morning summary', got %q", cronSt.createdJob.Prompt)
		}
	}
	// Verify AddJob was called on scheduler
	if len(sched.addedJobs) != 1 {
		t.Errorf("expected AddJob to be called once, got %d calls", len(sched.addedJobs))
	}
}

func TestCmdSchedule_MissingArgs(t *testing.T) {
	cronSt := &mockCronStoreAgent{}
	sched := &mockScheduler{}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, sched)
	cc.Args = "0 9 * * *" // only 5 fields, no prompt

	handler := makeCmdSchedule(cronSt, sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "Usage: /schedule") {
		t.Errorf("expected usage message, got %q", cr.messages[0])
	}
}

func TestCmdSchedule_InvalidCron(t *testing.T) {
	cronSt := &mockCronStoreAgent{}
	sched := &mockScheduler{}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, sched)
	cc.Args = "bad expr here and prompt text"

	handler := makeCmdSchedule(cronSt, sched)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "Invalid cron expression") {
		t.Errorf("expected error about invalid cron expression, got %q", cr.messages[0])
	}
}

// ---------------------------------------------------------------------------
// T4.5 — /history tests
// ---------------------------------------------------------------------------

func TestCmdHistory_WithResults(t *testing.T) {
	job := makeTestJob("gggggggg-0000-0000-0000-000000000006", "* * * * *", "ping task")
	cronSt := &mockCronStoreAgent{
		jobs: []store.CronJob{job},
		results: []store.CronResult{
			{ID: "r-1", JobID: job.ID, RanAt: time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC), Output: "ok output 1"},
			{ID: "r-2", JobID: job.ID, RanAt: time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC), Output: "ok output 2"},
			{ID: "r-3", JobID: job.ID, RanAt: time.Date(2026, 4, 12, 9, 0, 0, 0, time.UTC), ErrorMsg: "something failed"},
		},
	}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = "gggggggg"

	handler := makeCmdHistory(cronSt)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]
	// Should contain timestamps and status indicators
	if !strings.Contains(reply, "04-10") {
		t.Errorf("expected date '04-10' in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "ok") {
		t.Errorf("expected 'ok' status in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "error") {
		t.Errorf("expected 'error' status in reply, got:\n%s", reply)
	}
}

func TestCmdHistory_Empty(t *testing.T) {
	job := makeTestJob("hhhhhhhh-0000-0000-0000-000000000007", "* * * * *", "empty history task")
	cronSt := &mockCronStoreAgent{
		jobs:    []store.CronJob{job},
		results: []store.CronResult{}, // no results
	}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = "hhhhhhhh"

	handler := makeCmdHistory(cronSt)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "No runs found") {
		t.Errorf("expected 'No runs found' in reply, got %q", cr.messages[0])
	}
}

func TestCmdHistory_CustomLimit(t *testing.T) {
	job := makeTestJob("iiiiiiii-0000-0000-0000-000000000008", "* * * * *", "limit test task")
	// Insert 10 results
	results := make([]store.CronResult, 10)
	for i := range results {
		results[i] = store.CronResult{
			ID:     "result-" + string(rune('a'+i)),
			JobID:  job.ID,
			RanAt:  time.Now().Add(-time.Duration(10-i) * time.Minute),
			Output: "output",
		}
	}
	cronSt := &mockCronStoreAgent{
		jobs:    []store.CronJob{job},
		results: results,
	}

	cr := &capturedReply{}
	cc := makeCronCC(cr, cronSt, nil)
	cc.Args = "iiiiiiii 5"

	handler := makeCmdHistory(cronSt)
	if err := handler(cc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	// Should see "5 runs" in the header — ListResults is called with limit 5
	if !strings.Contains(cr.messages[0], "5 runs") {
		t.Errorf("expected '5 runs' in reply, got:\n%s", cr.messages[0])
	}
}
