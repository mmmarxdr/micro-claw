package agent

import (
	"testing"
	"time"
)

func TestRateLimiter_AllowsUpToMax(t *testing.T) {
	// Window: 1 minute, max 3 calls
	rl := newRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("expected Allow()=true on call %d, got false", i+1)
		}
	}
}

func TestRateLimiter_DeniesOnExceed(t *testing.T) {
	rl := newRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		rl.Allow()
	}

	if rl.Allow() {
		t.Error("expected Allow()=false on 4th call within window, got true")
	}
}

func TestRateLimiter_WindowSlides(t *testing.T) {
	// Use a very short window so we can test sliding without sleeping long.
	window := 50 * time.Millisecond
	rl := newRateLimiter(2, window)

	// Consume both slots.
	if !rl.Allow() {
		t.Fatal("first call should be allowed")
	}
	if !rl.Allow() {
		t.Fatal("second call should be allowed")
	}
	// Third call is denied.
	if rl.Allow() {
		t.Fatal("third call should be denied within window")
	}

	// Wait for the window to expire.
	time.Sleep(window + 10*time.Millisecond)

	// Now both slots are free — first call should be allowed again.
	if !rl.Allow() {
		t.Error("expected Allow()=true after window elapsed, got false")
	}
}

func TestRateLimiter_ZeroMax(t *testing.T) {
	// A limiter with maxCalls=0 should always deny.
	rl := newRateLimiter(0, time.Minute)
	if rl.Allow() {
		t.Error("expected Allow()=false with maxCalls=0")
	}
}

func TestRateLimiter_SingleSlot(t *testing.T) {
	window := 50 * time.Millisecond
	rl := newRateLimiter(1, window)

	if !rl.Allow() {
		t.Fatal("first call should be allowed")
	}
	if rl.Allow() {
		t.Fatal("second call should be denied")
	}

	time.Sleep(window + 10*time.Millisecond)

	if !rl.Allow() {
		t.Error("expected Allow()=true after window elapsed")
	}
}

func TestRateLimiter_NonBlocking(t *testing.T) {
	rl := newRateLimiter(1, time.Hour) // huge window — call 2 definitely denied
	rl.Allow()                          // consume the slot

	done := make(chan bool, 1)
	go func() {
		result := rl.Allow()
		done <- result
	}()

	select {
	case result := <-done:
		if result {
			t.Error("expected false for second call, got true")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Allow() blocked for more than 100ms — must be non-blocking")
	}
}
