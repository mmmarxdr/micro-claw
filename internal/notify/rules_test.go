package notify

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
)

// --- counting mock sender ---

type countingSender struct {
	mu    sync.Mutex
	calls []callRecord
	err   error
}

type callRecord struct {
	rule  config.NotificationRule
	event Event
}

func (c *countingSender) Send(_ context.Context, rule config.NotificationRule, event Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, callRecord{rule: rule, event: event})
	return c.err
}

func (c *countingSender) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *countingSender) ruleNames() []string { //nolint:unused // kept for future test assertions
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, len(c.calls))
	for i, cr := range c.calls {
		names[i] = cr.rule.Name
	}
	return names
}

// --- helpers ---

func newRule(name, eventType, jobID string, cooldownSec int) config.NotificationRule {
	return config.NotificationRule{
		Name:          name,
		EventType:     eventType,
		JobID:         jobID,
		TargetChannel: "telegram:1",
		CooldownSec:   cooldownSec,
	}
}

func newEvent(eventType, jobID string) Event {
	return Event{
		Type:      eventType,
		Origin:    OriginCron,
		JobID:     jobID,
		ChannelID: "cron:" + jobID,
		Timestamp: time.Now(),
	}
}

// waitForCalls polls until the sender has received at least n calls or deadline passes.
func waitForCalls(t *testing.T, s *countingSender, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d send calls (got %d)", n, s.count())
}

// buildEngine builds a RulesEngine backed by a countingSender wrapped in a
// real NotificationSender that uses the countingSender as its mux.
// Since NotificationSender.Send delegates to mux.Send, we use a thin wrapper.
func buildEngine(t *testing.T, rules []config.NotificationRule) (*RulesEngine, *countingSender) {
	t.Helper()
	cs := &countingSender{}
	// Wrap countingSender behind a channelSender-compatible adapter.
	mux := &csChannelAdapter{cs: cs}
	ns := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})
	engine, err := NewRulesEngine(rules, ns)
	if err != nil {
		t.Fatalf("NewRulesEngine: %v", err)
	}
	return engine, cs
}

// csChannelAdapter bridges countingSender to the channelSender interface.
type csChannelAdapter struct {
	cs *countingSender
}

func (a *csChannelAdapter) Send(_ context.Context, msg channel.OutgoingMessage) error {
	a.cs.mu.Lock()
	defer a.cs.mu.Unlock()
	a.cs.calls = append(a.cs.calls, callRecord{
		rule:  config.NotificationRule{Name: "_channel_", TargetChannel: msg.ChannelID},
		event: Event{Text: msg.Text},
	})
	return a.cs.err
}

// --- T2.4.1: rule matches correct event type ---

func TestRules_MatchByEventType(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "", 0),
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	waitForCalls(t, cs, 1, 500*time.Millisecond)

	if cs.count() != 1 {
		t.Errorf("expected 1 send call, got %d", cs.count())
	}
}

// --- T2.4.2: event type doesn't match any rule ---

func TestRules_NoMatch(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "", 0),
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobFired, "job-1")) // different event type

	// Give goroutines a moment to potentially fire.
	time.Sleep(50 * time.Millisecond)

	if cs.count() != 0 {
		t.Errorf("expected 0 send calls, got %d", cs.count())
	}
}

// --- T2.4.3: rule with job_id only matches that specific job ---

func TestRules_JobIDFilter(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "job-target", 0),
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobCompleted, "job-other"))
	time.Sleep(50 * time.Millisecond)
	if cs.count() != 0 {
		t.Errorf("job-other should not match, got %d calls", cs.count())
	}

	engine.Handle(newEvent(EventCronJobCompleted, "job-target"))
	waitForCalls(t, cs, 1, 500*time.Millisecond)
	if cs.count() != 1 {
		t.Errorf("job-target should match once, got %d calls", cs.count())
	}
}

// --- T2.4.4: rule fires once, then blocked by cooldown ---

func TestRules_Cooldown(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "", 60), // 60-second cooldown
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	waitForCalls(t, cs, 1, 500*time.Millisecond)

	// Second event should be suppressed by cooldown.
	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	time.Sleep(50 * time.Millisecond)

	if cs.count() != 1 {
		t.Errorf("expected cooldown to suppress second event, got %d calls", cs.count())
	}
}

// --- T2.4.5: rule fires again after cooldown expires ---

func TestRules_CooldownExpired(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "", 0), // 0 cooldown = always fire
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	waitForCalls(t, cs, 1, 500*time.Millisecond)

	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	waitForCalls(t, cs, 2, 500*time.Millisecond)

	if cs.count() != 2 {
		t.Errorf("expected 2 calls with zero cooldown, got %d", cs.count())
	}
}

// --- T2.4.6: two rules match same event, both fire ---

func TestRules_MultipleRules(t *testing.T) {
	rules := []config.NotificationRule{
		newRule("r1", EventCronJobCompleted, "", 0),
		newRule("r2", EventCronJobCompleted, "", 0),
	}
	engine, cs := buildEngine(t, rules)

	engine.Handle(newEvent(EventCronJobCompleted, "job-1"))
	waitForCalls(t, cs, 2, 500*time.Millisecond)

	if cs.count() != 2 {
		t.Errorf("expected 2 send calls (one per rule), got %d", cs.count())
	}
}

// --- T2.4.7: template was pre-compiled at construction time ---

func TestRules_TemplatePrecompiled(t *testing.T) {
	// Valid template — NewRulesEngine must succeed.
	rules := []config.NotificationRule{
		{
			Name:          "r1",
			EventType:     EventCronJobCompleted,
			TargetChannel: "telegram:1",
			Template:      "Job {{.JobID}} finished",
		},
	}
	cs := &countingSender{}
	mux := &csChannelAdapter{cs: cs}

	// Use a tracking auditor so we can verify the rendered text.
	var rendered atomic.Value
	renderedMux := &renderCaptureMux{inner: mux, capture: &rendered}

	ns := NewNotificationSender(renderedMux, audit.NoopAuditor{}, &mockBus{})

	engine, err := NewRulesEngine(rules, ns)
	if err != nil {
		t.Fatalf("NewRulesEngine with valid template should succeed, got: %v", err)
	}

	event := newEvent(EventCronJobCompleted, "job-42")
	engine.Handle(event)

	// Wait for goroutine.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if v := rendered.Load(); v != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	v := rendered.Load()
	if v == nil {
		t.Fatal("no send call captured")
	}
	want := "Job job-42 finished"
	if got := v.(string); got != want {
		t.Errorf("rendered text = %q, want %q", got, want)
	}
}

// TestRules_InvalidTemplate_ReturnsError verifies NewRulesEngine returns an error
// for invalid template syntax.
func TestRules_InvalidTemplate_ReturnsError(t *testing.T) {
	rules := []config.NotificationRule{
		{
			Name:          "bad-rule",
			EventType:     EventCronJobCompleted,
			TargetChannel: "telegram:1",
			Template:      "{{.UnclosedAction",
		},
	}
	cs := &countingSender{}
	mux := &csChannelAdapter{cs: cs}
	ns := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})

	_, err := NewRulesEngine(rules, ns)
	if err == nil {
		t.Fatal("expected error for invalid template, got nil")
	}

	var tpe *TemplateParseError
	if !errors.As(err, &tpe) {
		t.Errorf("expected *TemplateParseError, got %T: %v", err, err)
	}
}

// --- render capture helper ---

type renderCaptureMux struct {
	inner   channelSender
	capture *atomic.Value
}

func (r *renderCaptureMux) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	r.capture.Store(msg.Text)
	return r.inner.Send(ctx, msg)
}
