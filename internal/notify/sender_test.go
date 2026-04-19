package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
)

// --- mock channel sender ---

type mockSender struct {
	mu      sync.Mutex
	calls   []channel.OutgoingMessage
	errFunc func(msg channel.OutgoingMessage) error // nil = always succeed
}

func (m *mockSender) Send(_ context.Context, msg channel.OutgoingMessage) error {
	m.mu.Lock()
	m.calls = append(m.calls, msg)
	m.mu.Unlock()
	if m.errFunc != nil {
		return m.errFunc(msg)
	}
	return nil
}

func (m *mockSender) sentTo() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	for i, c := range m.calls {
		out[i] = c.ChannelID
	}
	return out
}

// --- mock auditor ---

type recordingAuditor struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (a *recordingAuditor) Emit(_ context.Context, ev audit.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}

func (a *recordingAuditor) Close() error { return nil }

func (a *recordingAuditor) last() (audit.AuditEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return audit.AuditEvent{}, false
	}
	return a.events[len(a.events)-1], true
}

// --- mock bus ---

type mockBus struct {
	mu     sync.Mutex
	events []Event
}

func (b *mockBus) Emit(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}
func (b *mockBus) Subscribe(_ func(Event)) {}
func (b *mockBus) Close()                  {}

// --- helpers ---

func makeEvent(channelID, text string) Event {
	return Event{
		Type:      EventCronJobCompleted,
		Origin:    OriginAgent,
		JobID:     "job-1",
		JobPrompt: "do something",
		ChannelID: channelID,
		Text:      text,
		Timestamp: time.Now(),
	}
}

func makeRule(name, target, fallback, tmpl string) config.NotificationRule {
	return config.NotificationRule{
		Name:            name,
		EventType:       EventCronJobCompleted,
		TargetChannel:   target,
		FallbackChannel: fallback,
		Template:        tmpl,
	}
}

// --- tests ---

// T2.3.1: basic success — sends to TargetChannel, audit event emitted.
func TestSender_Success(t *testing.T) {
	mux := &mockSender{}
	aud := &recordingAuditor{}
	bus := &mockBus{}

	s := NewNotificationSender(mux, aud, bus)
	rule := makeRule("r1", "telegram:123", "", "")
	event := makeEvent("cli", "hello")

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := mux.sentTo()
	if len(sent) != 1 || sent[0] != "telegram:123" {
		t.Fatalf("expected send to telegram:123, got %v", sent)
	}

	ev, ok := aud.last()
	if !ok {
		t.Fatal("no audit event emitted")
	}
	if ev.EventType != EventNotificationSent {
		t.Errorf("audit event type = %q, want %q", ev.EventType, EventNotificationSent)
	}
}

// T2.3.2: template with {{.JobPrompt}} renders correctly.
func TestSender_TemplateRender(t *testing.T) {
	mux := &mockSender{}
	s := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})

	rule := makeRule("r1", "telegram:123", "", "Job {{.JobID}} done: {{.JobPrompt}}")
	event := makeEvent("cli", "raw text")
	event.JobID = "job-42"
	event.JobPrompt = "run backup"

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mux.mu.Lock()
	sent := mux.calls[0].Text
	mux.mu.Unlock()

	want := "Job job-42 done: run backup"
	if sent != want {
		t.Errorf("rendered text = %q, want %q", sent, want)
	}
}

// T2.3.3: bad template falls back to event.Text.
func TestSender_TemplateError(t *testing.T) {
	mux := &mockSender{}
	s := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})

	// Template has an unclosed action — will fail to parse.
	rule := makeRule("r1", "telegram:123", "", "{{.UnclosedAction")
	event := makeEvent("cli", "fallback text")

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mux.mu.Lock()
	sent := mux.calls[0].Text
	mux.mu.Unlock()

	if sent != "fallback text" {
		t.Errorf("expected fallback text, got %q", sent)
	}
}

// T2.3.4: primary fails, fallback succeeds.
func TestSender_PrimaryFails_FallbackSucceeds(t *testing.T) {
	callCount := 0
	mux := &mockSender{
		errFunc: func(msg channel.OutgoingMessage) error {
			callCount++
			if msg.ChannelID == "telegram:bad" {
				return errors.New("telegram down")
			}
			return nil
		},
	}
	s := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})
	rule := makeRule("r1", "telegram:bad", "cli", "")
	event := makeEvent("cli", "hello")

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("expected nil error after fallback success, got %v", err)
	}

	sent := mux.sentTo()
	if len(sent) != 2 {
		t.Fatalf("expected 2 send attempts, got %d", len(sent))
	}
	if sent[0] != "telegram:bad" || sent[1] != "cli" {
		t.Errorf("send sequence = %v, want [telegram:bad cli]", sent)
	}
}

// T2.3.5: primary fails, fallback fails — error returned.
func TestSender_PrimaryFails_FallbackFails(t *testing.T) {
	mux := &mockSender{
		errFunc: func(_ channel.OutgoingMessage) error {
			return errors.New("all channels down")
		},
	}
	s := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})
	rule := makeRule("r1", "telegram:bad", "cli", "")
	event := makeEvent("cli", "hello")

	err := s.Send(context.Background(), rule, event)
	if err == nil {
		t.Fatal("expected error when both primary and fallback fail")
	}
}

// T2.3.6: rule has no TargetChannel — uses event.ChannelID.
func TestSender_NoTargetChannel_UsesEventChannel(t *testing.T) {
	mux := &mockSender{}
	s := NewNotificationSender(mux, audit.NoopAuditor{}, &mockBus{})
	rule := makeRule("r1", "", "", "") // no target channel
	event := makeEvent("telegram:999", "hello")

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := mux.sentTo()
	if len(sent) != 1 || sent[0] != "telegram:999" {
		t.Fatalf("expected send to telegram:999 (from event), got %v", sent)
	}
}

// T2.3.7: verify audit event fields (EventType, Details).
func TestSender_AuditEventShape(t *testing.T) {
	mux := &mockSender{}
	aud := &recordingAuditor{}
	s := NewNotificationSender(mux, aud, &mockBus{})

	rule := makeRule("my-rule", "telegram:123", "", "")
	event := makeEvent("cli", "hello")
	event.JobID = "job-99"
	event.Type = EventCronJobCompleted

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, ok := aud.last()
	if !ok {
		t.Fatal("no audit event emitted")
	}

	if ev.EventType != EventNotificationSent {
		t.Errorf("EventType = %q, want %q", ev.EventType, EventNotificationSent)
	}
	if ev.ScopeID != "notify" {
		t.Errorf("ScopeID = %q, want %q", ev.ScopeID, "notify")
	}
	if ev.Details["rule"] != "my-rule" {
		t.Errorf("Details[rule] = %q, want %q", ev.Details["rule"], "my-rule")
	}
	if ev.Details["target_channel"] != "telegram:123" {
		t.Errorf("Details[target_channel] = %q, want %q", ev.Details["target_channel"], "telegram:123")
	}
	if ev.Details["event_type"] != EventCronJobCompleted {
		t.Errorf("Details[event_type] = %q, want %q", ev.Details["event_type"], EventCronJobCompleted)
	}
	if ev.Details["job_id"] != "job-99" {
		t.Errorf("Details[job_id] = %q, want %q", ev.Details["job_id"], "job-99")
	}
	// All four required detail keys must be present (even if empty).
	for _, key := range []string{"rule", "target_channel", "event_type", "job_id"} {
		if _, ok := ev.Details[key]; !ok {
			t.Errorf("Details missing required key %q", key)
		}
	}
}

// T6.1: notification.sent and notification.failed audit events have correct shape.

// TestSend_AuditSent verifies a successful send emits notification.sent with all required fields.
func TestSend_AuditSent(t *testing.T) {
	mux := &mockSender{}
	aud := &recordingAuditor{}
	s := NewNotificationSender(mux, aud, &mockBus{})

	rule := makeRule("audit-rule", "telegram:42", "", "")
	event := makeEvent("cli", "hello")
	event.JobID = "job-audit"

	if err := s.Send(context.Background(), rule, event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ev, ok := aud.last()
	if !ok {
		t.Fatal("no audit event emitted")
	}
	if ev.EventType != EventNotificationSent {
		t.Errorf("EventType = %q, want %q", ev.EventType, EventNotificationSent)
	}
	if ev.ScopeID != "notify" {
		t.Errorf("ScopeID = %q, want %q", ev.ScopeID, "notify")
	}
	// All four required detail keys must be present.
	for _, key := range []string{"rule", "target_channel", "event_type", "job_id"} {
		if _, ok := ev.Details[key]; !ok {
			t.Errorf("Details missing required key %q", key)
		}
	}
}

// TestSend_AuditFailed verifies a failed send emits notification.failed.
func TestSend_AuditFailed(t *testing.T) {
	mux := &mockSender{
		errFunc: func(_ channel.OutgoingMessage) error {
			return errors.New("channel unavailable")
		},
	}
	aud := &recordingAuditor{}
	s := NewNotificationSender(mux, aud, &mockBus{})

	rule := makeRule("fail-rule", "telegram:bad", "", "") // no fallback
	event := makeEvent("cli", "hello")

	_ = s.Send(context.Background(), rule, event) // error expected

	ev, ok := aud.last()
	if !ok {
		t.Fatal("no audit event emitted on failure")
	}
	if ev.EventType != EventNotificationFailed {
		t.Errorf("EventType = %q, want %q", ev.EventType, EventNotificationFailed)
	}
	if ev.ScopeID != "notify" {
		t.Errorf("ScopeID = %q, want %q", ev.ScopeID, "notify")
	}
}
