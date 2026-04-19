package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"daimon/internal/store"
)

// ─── mock store ──────────────────────────────────────────────────────────────

// mockOutputStore is a minimal store.OutputStore for testing.
type mockOutputStore struct {
	mu      sync.Mutex
	indexed []store.ToolOutput
	indexFn func(ctx context.Context, out store.ToolOutput) error // nil → always succeed
}

func (m *mockOutputStore) IndexOutput(ctx context.Context, out store.ToolOutput) error {
	if m.indexFn != nil {
		if err := m.indexFn(ctx, out); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexed = append(m.indexed, out)
	return nil
}

func (m *mockOutputStore) SearchOutputs(_ context.Context, _ string, _ int) ([]store.ToolOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]store.ToolOutput, len(m.indexed))
	copy(cp, m.indexed)
	return cp, nil
}

func (m *mockOutputStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.indexed)
}

// ─── log capture helper ───────────────────────────────────────────────────────

// captureLog redirects the default slog logger into buf for the duration of the
// test, then restores the original logger in a t.Cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// makeOutput returns a minimal ToolOutput with the given id/name.
func makeOutput(id, name string) store.ToolOutput {
	return store.ToolOutput{
		ID:        id,
		ToolName:  name,
		Content:   "output content",
		Timestamp: time.Now().UTC(),
	}
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestIndexingWorker_HappyPath: enqueue 10 items; verify all 10 are indexed.
func TestIndexingWorker_HappyPath(t *testing.T) {
	t.Parallel()

	ms := &mockOutputStore{}
	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	const n = 10
	for i := 0; i < n; i++ {
		w.Enqueue(makeOutput("id"+string(rune('0'+i)), "tool"))
	}
	w.Stop()

	if got := ms.count(); got != n {
		t.Errorf("indexed %d items, want %d", got, n)
	}
}

// TestIndexingWorker_ChannelFull_Drops: blocking store fills the buffer; extras are dropped with warning.
// NOT parallel: mutates the global slog logger, which would race with other parallel tests.
func TestIndexingWorker_ChannelFull_Drops(t *testing.T) {
	logBuf := captureLog(t)

	unblock := make(chan struct{})
	var inFlight atomic.Int64

	ms := &mockOutputStore{
		indexFn: func(ctx context.Context, out store.ToolOutput) error {
			inFlight.Add(1)
			<-unblock // block until released
			inFlight.Add(-1)
			return nil
		},
	}

	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	// Enqueue 300 items. The buffer holds 256. The first one enters the worker
	// goroutine immediately (goroutine is blocked on store call), so the channel
	// can hold 256 more — total 257 accepted, 43 dropped. In practice the exact
	// split depends on scheduling, so we just require no panic and that warnings
	// were emitted.
	const total = 300
	for i := 0; i < total; i++ {
		id := "id" + string(rune('a'+i%26)) + string(rune('0'+i%10))
		w.Enqueue(makeOutput(id, "tool"))
	}

	// Wait until the goroutine is blocked inside the store call.
	deadline := time.Now().Add(2 * time.Second)
	for inFlight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// Release the blocker and stop.
	close(unblock)
	w.Stop()

	// Verify warnings were logged.
	logStr := logBuf.String()
	if !strings.Contains(logStr, "channel full") {
		t.Errorf("expected 'channel full' warning in log, got:\n%s", logStr)
	}
}

// TestIndexingWorker_StoreError_IsolatedPerItem: errors on every other call; all items attempted.
func TestIndexingWorker_StoreError_IsolatedPerItem(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	var successes atomic.Int64
	var errs atomic.Int64

	ms := &mockOutputStore{
		indexFn: func(ctx context.Context, out store.ToolOutput) error {
			n := attempts.Add(1)
			if n%2 == 0 {
				errs.Add(1)
				return errors.New("store error")
			}
			successes.Add(1)
			return nil
		},
	}

	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	const n = 10
	for i := 0; i < n; i++ {
		w.Enqueue(makeOutput("id"+string(rune('0'+i)), "tool"))
	}
	w.Stop()

	total := successes.Load() + errs.Load()
	if total != n {
		t.Errorf("total attempts = %d, want %d (successes=%d errs=%d)",
			total, n, successes.Load(), errs.Load())
	}
}

// TestIndexingWorker_Stop_DrainsPending: items enqueued before Stop should all be indexed.
func TestIndexingWorker_Stop_DrainsPending(t *testing.T) {
	t.Parallel()

	ms := &mockOutputStore{}
	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	const n = 50
	for i := 0; i < n; i++ {
		w.Enqueue(makeOutput("id"+string(rune('0'+i%10))+string(rune('a'+i%26)), "tool"))
	}
	w.Stop() // must block until drain is complete

	if got := ms.count(); got != n {
		t.Errorf("after Stop: indexed %d, want %d", got, n)
	}
}

// TestIndexingWorker_Stop_Idempotent: calling Stop twice must not panic.
func TestIndexingWorker_Stop_Idempotent(t *testing.T) {
	t.Parallel()

	ms := &mockOutputStore{}
	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	w.Stop()
	w.Stop() // second call — must not panic
}

// TestIndexingWorker_Enqueue_After_Stop: Enqueue after Stop must not panic.
func TestIndexingWorker_Enqueue_After_Stop(t *testing.T) {
	t.Parallel()

	ms := &mockOutputStore{}
	w := NewIndexingWorker(ms)
	w.Start(context.Background())
	w.Stop()

	// Must not panic.
	w.Enqueue(makeOutput("post-stop", "tool"))
}

// TestIndexingWorker_ShutdownDrain_Timeout: blocking store triggers the 2s drain timeout.
func TestIndexingWorker_ShutdownDrain_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow drain-timeout test in short mode")
	}
	t.Parallel()

	// Each IndexOutput takes 3s — longer than the 2s drain timeout.
	ms := &mockOutputStore{
		indexFn: func(ctx context.Context, out store.ToolOutput) error {
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			return nil
		},
	}

	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	// Enqueue enough items to fill the goroutine + some buffer.
	for i := 0; i < 10; i++ {
		w.Enqueue(makeOutput("id"+string(rune('0'+i)), "slow"))
	}

	start := time.Now()
	w.Stop() // should return in ~2s (drain timeout), not 30s
	elapsed := time.Since(start)

	if elapsed > 2500*time.Millisecond {
		t.Errorf("Stop took %v, expected ≤2.5s (drain timeout should have fired)", elapsed)
	}
}

// TestIndexingWorker_Race: concurrent Enqueue calls under -race.
func TestIndexingWorker_Race(t *testing.T) {
	t.Parallel()

	ms := &mockOutputStore{}
	w := NewIndexingWorker(ms)
	w.Start(context.Background())

	const goroutines = 20
	const perGoroutine = 50

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := "g" + string(rune('0'+g%10)) + "i" + string(rune('0'+i%10))
				w.Enqueue(makeOutput(id, "race_tool"))
			}
		}()
	}
	wg.Wait()
	w.Stop()

	// We don't assert exact count (drops are expected when channel is full),
	// but there must be no data race and no panic.
}
