package cron

import (
	"context"
	"strings"
	"testing"
	"time"

	"daimon/internal/channel"
	"daimon/internal/store"
)

// ─── mock SchedulerIface for CronChannel tests ────────────────────────────────

type mockSchedulerIface struct {
	startCalled bool
	stopCalled  bool
	startErr    error
}

func (m *mockSchedulerIface) Start(_ context.Context, _ chan<- channel.IncomingMessage) error {
	m.startCalled = true
	return m.startErr
}

func (m *mockSchedulerIface) Stop() {
	m.stopCalled = true
}

func (m *mockSchedulerIface) AddJob(_ context.Context, _ store.CronJob) error { return nil }

func (m *mockSchedulerIface) RemoveJob(_ context.Context, _ string) error { return nil }

func (m *mockSchedulerIface) ListActiveJobs(_ context.Context) ([]ActiveJob, error) {
	return nil, nil
}

// TestCronChannel_Send_StoresResult verifies that Send persists a CronResult.
func TestCronChannel_Send_StoresResult(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	// Pre-populate the job so GetJob succeeds.
	job := makeJob("send-job-1", "0 * * * *")
	st.jobs["send-job-1"] = job

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()
	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	ch := NewCronChannel(sched, st, nil, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:send-job-1",
		Text:      "job output text",
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(st.results) != 1 {
		t.Fatalf("expected 1 result saved, got %d", len(st.results))
	}
	r := st.results[0]
	if r.JobID != "send-job-1" {
		t.Errorf("expected JobID=send-job-1, got %q", r.JobID)
	}
	if r.Output != "job output text" {
		t.Errorf("expected Output=%q, got %q", "job output text", r.Output)
	}
	if r.ID == "" {
		t.Error("expected result ID to be set (UUID)")
	}
}

// TestCronChannel_Send_CallsOrigSender verifies that origSender is called with
// the job's channel_id when the job has one configured.
func TestCronChannel_Send_CallsOrigSender(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := store.CronJob{
		ID:            "fwd-job",
		Schedule:      "0 * * * *",
		ScheduleHuman: "hourly",
		Prompt:        "summarize",
		ChannelID:     "telegram:99999",
		Enabled:       true,
		CreatedAt:     time.Now().UTC(),
	}
	st.jobs["fwd-job"] = job

	var capturedMsg channel.OutgoingMessage
	origSender := func(_ context.Context, msg channel.OutgoingMessage) error {
		capturedMsg = msg
		return nil
	}

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()
	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	ch := NewCronChannel(sched, st, origSender, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:fwd-job",
		Text:      "forwarded output",
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if capturedMsg.ChannelID != "telegram:99999" {
		t.Errorf("expected origSender called with ChannelID=telegram:99999, got %q", capturedMsg.ChannelID)
	}
	if capturedMsg.Text != "forwarded output" {
		t.Errorf("expected origSender called with Text=%q, got %q", "forwarded output", capturedMsg.Text)
	}
}

// TestCronChannel_Send_NoOrigSender_NoError verifies that nil origSender is fine.
func TestCronChannel_Send_NoOrigSender_NoError(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := makeJob("no-sender-job", "0 * * * *")
	job.ChannelID = "cli"
	st.jobs["no-sender-job"] = job

	sched := NewScheduler(st, time.UTC, 30, 50)
	inbox := makeInbox()
	if err := sched.Start(ctx, inbox); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	ch := NewCronChannel(sched, st, nil, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:no-sender-job",
		Text:      "some output",
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Errorf("expected no error with nil origSender, got %v", err)
	}
}

// ─── CronChannel.Start and Stop tests ─────────────────────────────────────────

// TestCronChannel_Start_CallsSchedulerStart verifies that CronChannel.Start
// delegates to the underlying scheduler's Start method.
func TestCronChannel_Start_CallsSchedulerStart(t *testing.T) {
	st := newStubStore()
	mockSched := &mockSchedulerIface{}
	ch := NewCronChannel(mockSched, st, nil, false)

	inbox := make(chan channel.IncomingMessage, 10)
	if err := ch.Start(context.Background(), inbox); err != nil {
		t.Fatalf("Start returned unexpected error: %v", err)
	}
	if !mockSched.startCalled {
		t.Error("expected scheduler.Start to be called")
	}
}

// TestCronChannel_Stop_CallsSchedulerStop verifies that CronChannel.Stop
// delegates to the underlying scheduler's Stop method.
func TestCronChannel_Stop_CallsSchedulerStop(t *testing.T) {
	st := newStubStore()
	mockSched := &mockSchedulerIface{}
	ch := NewCronChannel(mockSched, st, nil, false)

	if err := ch.Stop(); err != nil {
		t.Fatalf("Stop returned unexpected error: %v", err)
	}
	if !mockSched.stopCalled {
		t.Error("expected scheduler.Stop to be called")
	}
}

// TestCronChannel_Name verifies the channel name is "cron".
func TestCronChannel_Name(t *testing.T) {
	ch := NewCronChannel(&mockSchedulerIface{}, newStubStore(), nil, false)
	if ch.Name() != "cron" {
		t.Errorf("expected Name()=%q, got %q", "cron", ch.Name())
	}
}

// TestCronChannel_Start_PropagatesError verifies that a scheduler Start error
// is propagated back to the caller.
func TestCronChannel_Start_PropagatesError(t *testing.T) {
	st := newStubStore()
	mockSched := &mockSchedulerIface{startErr: store.ErrNotFound}
	ch := NewCronChannel(mockSched, st, nil, false)

	inbox := make(chan channel.IncomingMessage, 10)
	err := ch.Start(context.Background(), inbox)
	if err == nil {
		t.Fatal("expected error from Start but got nil")
	}
}

// TestCronChannel_Send_NotifyOnCompletion_AddsHeader verifies that when
// notifyOnCompletion=true, the forwarded text includes the task header prefix.
func TestCronChannel_Send_NotifyOnCompletion_AddsHeader(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := store.CronJob{
		ID:        "hdr-job",
		Schedule:  "0 * * * *",
		Prompt:    "send daily report",
		ChannelID: "cli",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}
	st.jobs["hdr-job"] = job

	var capturedText string
	origSender := func(_ context.Context, msg channel.OutgoingMessage) error {
		capturedText = msg.Text
		return nil
	}

	ch := NewCronChannel(&mockSchedulerIface{}, st, origSender, true)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:hdr-job",
		Text:      "report output here",
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	const wantPrefix = "📋 Scheduled task 'send daily report':\n\n"
	if !strings.HasPrefix(capturedText, wantPrefix) {
		t.Errorf("expected forwarded text to have prefix %q, got %q", wantPrefix, capturedText)
	}
	if !strings.HasSuffix(capturedText, "report output here") {
		t.Errorf("expected forwarded text to end with %q, got %q", "report output here", capturedText)
	}
}

// TestCronChannel_Send_NotifyOnCompletion_False_NoHeader verifies that when
// notifyOnCompletion=false, the raw output is forwarded without a header.
func TestCronChannel_Send_NotifyOnCompletion_False_NoHeader(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := store.CronJob{
		ID:        "nohdr-job",
		Schedule:  "0 * * * *",
		Prompt:    "some task",
		ChannelID: "cli",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}
	st.jobs["nohdr-job"] = job

	var capturedText string
	origSender := func(_ context.Context, msg channel.OutgoingMessage) error {
		capturedText = msg.Text
		return nil
	}

	ch := NewCronChannel(&mockSchedulerIface{}, st, origSender, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:nohdr-job",
		Text:      "plain output",
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if capturedText != "plain output" {
		t.Errorf("expected forwarded text=%q, got %q", "plain output", capturedText)
	}
}

// TestCronChannel_Send_ErrorMetadata_AddsWarningPrefix verifies that messages
// marked with cron_error=true are forwarded with the "⚠️ Task '...' failed:" prefix.
func TestCronChannel_Send_ErrorMetadata_AddsWarningPrefix(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := store.CronJob{
		ID:        "err-job",
		Schedule:  "0 * * * *",
		Prompt:    "run analysis",
		ChannelID: "cli",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}
	st.jobs["err-job"] = job

	var capturedText string
	origSender := func(_ context.Context, msg channel.OutgoingMessage) error {
		capturedText = msg.Text
		return nil
	}

	ch := NewCronChannel(&mockSchedulerIface{}, st, origSender, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:err-job",
		Text:      "provider timed out",
		Metadata:  map[string]string{"cron_error": "true"},
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	const wantPrefix = "⚠️ Task 'run analysis' failed:"
	if !strings.HasPrefix(capturedText, wantPrefix) {
		t.Errorf("expected forwarded text to have prefix %q, got %q", wantPrefix, capturedText)
	}
}

// TestCronChannel_Send_ErrorMetadata_StoresInErrorMsg verifies that error messages
// are persisted with ErrorMsg set (not Output).
func TestCronChannel_Send_ErrorMetadata_StoresInErrorMsg(t *testing.T) {
	st := newStubStore()
	ctx := context.Background()

	job := makeJob("errstore-job", "0 * * * *")
	job.ChannelID = "cli"
	st.jobs["errstore-job"] = job

	ch := NewCronChannel(&mockSchedulerIface{}, st, nil, false)

	msg := channel.OutgoingMessage{
		ChannelID: "cron:errstore-job",
		Text:      "something went wrong",
		Metadata:  map[string]string{"cron_error": "true"},
	}
	if err := ch.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(st.results) != 1 {
		t.Fatalf("expected 1 result saved, got %d", len(st.results))
	}
	r := st.results[0]
	if r.ErrorMsg != "something went wrong" {
		t.Errorf("expected ErrorMsg=%q, got %q", "something went wrong", r.ErrorMsg)
	}
	if r.Output != "" {
		t.Errorf("expected Output to be empty for error result, got %q", r.Output)
	}
}
