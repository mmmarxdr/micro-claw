package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/skill"
)

// TestAgent_Semaphore_Capacity verifies that at most maxConcurrent messages
// are processed simultaneously.
func TestAgent_Semaphore_Capacity(t *testing.T) {
	const maxConcurrent = 2
	const numMessages = 4

	// Gate controls when processMessage is allowed to return.
	gate := make(chan struct{})

	// inFlight tracks the peak concurrent execution count.
	var current int64
	var peak int64

	// Provider that blocks until gate is closed.
	blockingProv := &blockingProvider{
		gate: gate,
		onEnter: func() {
			c := atomic.AddInt64(&current, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if c <= p || atomic.CompareAndSwapInt64(&peak, p, c) {
					break
				}
			}
		},
		onExit: func() {
			atomic.AddInt64(&current, -1)
		},
	}

	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, blockingProv, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, maxConcurrent, false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inbox := make(chan channel.IncomingMessage, numMessages)
	for i := 0; i < numMessages; i++ {
		inbox <- channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("msg")}
	}

	// Start dispatching goroutines manually (mirrors Run() inner loop logic).
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for i := 0; i < numMessages; i++ {
			msg := <-inbox
			go func(m channel.IncomingMessage) {
				select {
				case ag.sem <- struct{}{}:
					defer func() { <-ag.sem }()
					ag.processMessage(ctx, m)
				case <-time.After(5 * time.Second):
					t.Errorf("semaphore acquire timed out")
				}
			}(msg)
		}
	}()

	// Give goroutines time to pile up.
	time.Sleep(100 * time.Millisecond)

	// Verify at most maxConcurrent are running concurrently.
	currentNow := atomic.LoadInt64(&current)
	if currentNow > maxConcurrent {
		t.Errorf("expected at most %d concurrent, but %d are running", maxConcurrent, currentNow)
	}

	// Unblock all waiting goroutines.
	close(gate)

	// Wait for all to finish.
	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatching goroutines did not finish")
	}

	// Allow in-flight processMessage calls to complete.
	time.Sleep(200 * time.Millisecond)

	// Verify peak was never above maxConcurrent.
	if p := atomic.LoadInt64(&peak); p > maxConcurrent {
		t.Errorf("peak concurrent = %d, want <= %d", p, maxConcurrent)
	}
}

// ---------------------------------------------------------------------------
// blockingProvider blocks in Chat() until gate is closed.
// ---------------------------------------------------------------------------

type blockingProvider struct {
	gate    chan struct{}
	onEnter func()
	onExit  func()
}

func (b *blockingProvider) Name() string                                    { return "blocking" }
func (b *blockingProvider) Model() string                                   { return "mock-model" }
func (b *blockingProvider) SupportsTools() bool                             { return false }
func (b *blockingProvider) SupportsMultimodal() bool                        { return false }
func (b *blockingProvider) SupportsAudio() bool                             { return false }
func (b *blockingProvider) HealthCheck(ctx context.Context) (string, error) { return "blocking", nil }
func (b *blockingProvider) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	if b.onEnter != nil {
		b.onEnter()
	}
	defer func() {
		if b.onExit != nil {
			b.onExit()
		}
	}()

	select {
	case <-b.gate:
	case <-ctx.Done():
	}

	return &provider.ChatResponse{Content: "done"}, nil
}
