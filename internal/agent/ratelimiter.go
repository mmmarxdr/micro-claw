package agent

import (
	"sync"
	"time"
)

// rateLimiter implements a sliding-window rate limiter using a circular buffer
// of timestamps. Zero-allocation after init.
type rateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	maxCalls int
	calls    []time.Time // circular buffer, length == maxCalls
	pos      int         // next write position in the circular buffer
}

// newRateLimiter creates a sliding-window rate limiter that allows at most
// maxCalls calls within the given window duration.
func newRateLimiter(maxCalls int, window time.Duration) *rateLimiter {
	var calls []time.Time
	if maxCalls > 0 {
		calls = make([]time.Time, maxCalls)
	}
	return &rateLimiter{
		window:   window,
		maxCalls: maxCalls,
		calls:    calls,
	}
}

// Allow returns true if the call is permitted under the rate limit.
// It is non-blocking and safe for concurrent use.
func (r *rateLimiter) Allow() bool {
	if r.maxCalls <= 0 {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-r.window)

	// The oldest slot in the circular buffer is at position r.pos.
	// If that oldest timestamp is within the current window, the buffer is full.
	oldest := r.calls[r.pos]
	if !oldest.IsZero() && oldest.After(windowStart) {
		// All maxCalls slots are within the window — deny.
		return false
	}

	// Slot is available (either zero or outside window). Record this call.
	r.calls[r.pos] = now
	r.pos = (r.pos + 1) % r.maxCalls
	return true
}
