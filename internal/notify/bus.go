package notify

import (
	"log/slog"
	"sync"
	"time"
)

// Event is the data shape passed through the bus.
type Event struct {
	Type      string            `json:"type"`
	Origin    Origin            `json:"origin"`
	JobID     string            `json:"job_id,omitempty"`
	JobPrompt string            `json:"job_prompt,omitempty"`
	ChannelID string            `json:"channel_id"`
	Text      string            `json:"text,omitempty"`
	Error     string            `json:"error,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Bus is the central event distribution interface. It is safe for concurrent use.
type Bus interface {
	// Emit enqueues an event for async delivery. Non-blocking: if the internal
	// buffer is full, the event is dropped and a WARN is logged. Must never
	// block the caller.
	Emit(event Event)

	// Subscribe registers a handler function to be called for every event that
	// passes the circuit breaker. Should be called during setup before events
	// start flowing.
	Subscribe(handler func(Event))

	// Close drains the internal channel and stops the worker goroutine.
	// Blocks until the worker exits. Idempotent — safe to call multiple times.
	Close()
}

// EventBus is the concrete Bus implementation.
type EventBus struct {
	ch       chan Event
	handlers []func(Event)
	mu       sync.Mutex
	done     chan struct{}

	// Circuit breaker state (protected by mu).
	maxPerMin   int
	sentCount   int
	windowStart time.Time

	// handlerTimeout is the maximum time each handler may run.
	handlerTimeout time.Duration

	// closeMu protects against concurrent Emit/Close races.
	// Emit holds a read lock; Close holds a write lock.
	closeMu sync.RWMutex
	closed  bool

	// closeOnce ensures Close is idempotent.
	closeOnce sync.Once
}

// NewEventBus creates and starts an EventBus.
//   - bufferSize: internal channel buffer; if <= 0 defaults to 256.
//   - maxPerMin: circuit breaker cap per 60-second window; if <= 0 defaults to 30.
//   - handlerTimeout: max duration each handler may run; if <= 0 defaults to 5s.
//
// The worker goroutine is started immediately.
func NewEventBus(bufferSize, maxPerMin int, handlerTimeout time.Duration) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	if maxPerMin <= 0 {
		maxPerMin = 30
	}
	if handlerTimeout <= 0 {
		handlerTimeout = 5 * time.Second
	}

	b := &EventBus{
		ch:             make(chan Event, bufferSize),
		done:           make(chan struct{}),
		maxPerMin:      maxPerMin,
		handlerTimeout: handlerTimeout,
		windowStart:    time.Now(),
	}
	go b.worker()
	return b
}

// Emit enqueues an event. Non-blocking: if the buffer is full, the event is
// dropped and a warning is logged. Safe for concurrent use. If the bus is
// already closed, the event is silently dropped.
func (b *EventBus) Emit(event Event) {
	b.closeMu.RLock()
	defer b.closeMu.RUnlock()
	if b.closed {
		return
	}
	select {
	case b.ch <- event:
	default:
		slog.Warn("notify: bus buffer full, dropping event", "type", event.Type)
	}
}

// Subscribe registers a handler. Thread-safe, but intended to be called
// during setup before events start flowing.
func (b *EventBus) Subscribe(fn func(Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, fn)
}

// Close drains the channel and waits for the worker goroutine to exit.
// Idempotent — subsequent calls are no-ops.
func (b *EventBus) Close() {
	b.closeOnce.Do(func() {
		// Acquire write lock to prevent concurrent Emit calls while we close.
		b.closeMu.Lock()
		b.closed = true
		close(b.ch)
		b.closeMu.Unlock()
	})
	<-b.done
}

// worker is the single goroutine that drains the event channel.
func (b *EventBus) worker() {
	defer close(b.done)

	for event := range b.ch {
		// Loop prevention: drop events emitted by the notification system itself.
		if event.Origin == OriginNotification {
			continue
		}

		// Circuit breaker: sliding 60-second window.
		b.mu.Lock()
		if time.Since(b.windowStart) > 60*time.Second {
			b.sentCount = 0
			b.windowStart = time.Now()
		}
		if b.sentCount >= b.maxPerMin {
			b.mu.Unlock()
			slog.Warn("notify: circuit breaker tripped, dropping event",
				"type", event.Type,
				"count", b.sentCount,
				"max_per_min", b.maxPerMin,
			)
			continue
		}
		b.sentCount++

		// Copy handler slice under lock to avoid holding lock during calls.
		handlers := make([]func(Event), len(b.handlers))
		copy(handlers, b.handlers)
		b.mu.Unlock()

		// Call each handler. Handlers are expected to be fast (they launch
		// goroutines internally). We call them directly in the worker goroutine
		// with a timeout enforced by a separate goroutine.
		for _, fn := range handlers {
			b.callWithTimeout(fn, event)
		}
	}
}

// callWithTimeout calls fn(event) and abandons it if it exceeds handlerTimeout.
// NOTE: when the timeout fires, the goroutine running fn(event) is abandoned and
// will continue running until fn returns on its own. Handlers that launch internal
// goroutines (e.g. RulesEngine.Handle) return quickly and are not affected. Only
// handlers that block for long durations without respecting a context will leak.
// To make handlers context-aware, extend the Bus interface to pass a context.
func (b *EventBus) callWithTimeout(fn func(Event), event Event) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(event)
	}()

	select {
	case <-done:
		// Handler completed normally.
	case <-time.After(b.handlerTimeout):
		slog.Warn("notify: handler timed out", "type", event.Type, "timeout", b.handlerTimeout)
	}
}
