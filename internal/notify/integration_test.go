package notify

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
)

// integSender is a channelSender mock used only in integration tests.
type integSender struct {
	mu    sync.Mutex
	calls []channel.OutgoingMessage
	done  chan struct{} // closed on first call
	once  sync.Once
}

func newIntegSender() *integSender {
	return &integSender{done: make(chan struct{})}
}

func (s *integSender) Send(_ context.Context, msg channel.OutgoingMessage) error {
	s.mu.Lock()
	s.calls = append(s.calls, msg)
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *integSender) callCount() int { //nolint:unused // kept for future test assertions
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// waitFirst blocks until at least one Send call arrives or timeout elapses.
func (s *integSender) waitFirst(timeout time.Duration) bool {
	select {
	case <-s.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestBusRulesEngine_EndToEnd creates a real EventBus and RulesEngine with a
// mock channelSender, emits a matching event, and verifies the sender is called
// with the correct channel/text arguments.
func TestBusRulesEngine_EndToEnd(t *testing.T) {
	rules := []config.NotificationRule{
		{
			Name:          "fire-rule",
			EventType:     EventCronJobFired,
			TargetChannel: "telegram:notify",
			Template:      "Job {{.JobID}} fired",
		},
	}

	cs := newIntegSender()
	bus := NewEventBus(256, 30, 5*time.Second)
	defer bus.Close()

	sender := NewNotificationSender(cs, audit.NoopAuditor{}, bus)
	engine, err := NewRulesEngine(rules, sender)
	if err != nil {
		t.Fatalf("NewRulesEngine error: %v", err)
	}
	bus.Subscribe(engine.Handle)

	// Emit a matching event.
	bus.Emit(Event{
		Type:      EventCronJobFired,
		Origin:    OriginCron,
		JobID:     "job-e2e",
		JobPrompt: "run backup",
		ChannelID: "cron:job-e2e",
		Timestamp: time.Now(),
	})

	if !cs.waitFirst(2 * time.Second) {
		t.Fatal("timeout: sender not called within 2s")
	}

	cs.mu.Lock()
	call := cs.calls[0]
	cs.mu.Unlock()

	if call.ChannelID != "telegram:notify" {
		t.Errorf("ChannelID = %q, want %q", call.ChannelID, "telegram:notify")
	}
	want := "Job job-e2e fired"
	if call.Text != want {
		t.Errorf("Text = %q, want %q", call.Text, want)
	}
}

// TestBusRulesEngine_CircuitBreaker verifies that with maxPerMinute=3,
// emitting 10 events results in at most 3 reaching the underlying channel sender.
func TestBusRulesEngine_CircuitBreaker(t *testing.T) {
	rules := []config.NotificationRule{
		{
			Name:          "cb-rule",
			EventType:     EventCronJobFired,
			TargetChannel: "telegram:notify",
		},
	}

	var callCount atomic.Int64
	cs := &atomicChannelSender{counter: &callCount}

	bus := NewEventBus(256, 3, 5*time.Second)

	sender := NewNotificationSender(cs, audit.NoopAuditor{}, bus)
	engine, err := NewRulesEngine(rules, sender)
	if err != nil {
		t.Fatalf("NewRulesEngine error: %v", err)
	}
	bus.Subscribe(engine.Handle)

	// Emit 10 events rapidly.
	for i := 0; i < 10; i++ {
		bus.Emit(Event{
			Type:      EventCronJobFired,
			Origin:    OriginCron,
			JobID:     "job-cb",
			ChannelID: "cron:job-cb",
			Timestamp: time.Now(),
		})
	}

	// Close the bus — drains all buffered events and blocks until worker exits.
	bus.Close()

	// Allow goroutines spawned by Handle (go sender.Send(...)) to complete.
	time.Sleep(100 * time.Millisecond)

	got := int(callCount.Load())
	if got > 3 {
		t.Errorf("circuit breaker: sender called %d times, want at most 3", got)
	}
	if got == 0 {
		t.Errorf("circuit breaker: sender never called (expected 1-3 calls)")
	}
}

// atomicChannelSender counts calls using an atomic counter.
type atomicChannelSender struct {
	counter *atomic.Int64
}

func (a *atomicChannelSender) Send(_ context.Context, _ channel.OutgoingMessage) error {
	a.counter.Add(1)
	return nil
}
