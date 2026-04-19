package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"daimon/internal/store"
)

// IndexingWorker asynchronously indexes tool outputs into an OutputStore.
// Enqueue is non-blocking: a full channel drops the item with a warning.
// Enqueue after Stop is safe — items are silently dropped via the stopped channel.
type IndexingWorker struct {
	store    store.OutputStore
	ch       chan store.ToolOutput
	wg       sync.WaitGroup
	stopOnce sync.Once
	stopped  chan struct{} // closed when Stop() is first called; guards Enqueue-after-Stop
	closed   chan struct{} // closed when loop() exits (drain complete)
}

// NewIndexingWorker constructs a worker with a 256-item buffer.
func NewIndexingWorker(s store.OutputStore) *IndexingWorker {
	return &IndexingWorker{
		store:   s,
		ch:      make(chan store.ToolOutput, 256),
		stopped: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

// Start launches the single consumer goroutine. Call once.
func (w *IndexingWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.loop(ctx)
}

// loop reads from ch until it is closed; each item is indexed with failure isolation.
func (w *IndexingWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	defer close(w.closed)
	for out := range w.ch {
		// Use a fresh context with short deadline so a broken store does not
		// hang the worker. Failure isolation: one error never stops the loop.
		indexCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := w.store.IndexOutput(indexCtx, out); err != nil {
			slog.Warn("indexing_worker: failed to index output",
				"error", err,
				"tool", out.ToolName,
				"id", out.ID,
			)
		}
		cancel()
		_ = ctx // ctx intentionally unused inside loop; failure isolation requires fresh ctx
	}
}

// Enqueue submits an output for async indexing.
// Returns immediately. Three outcomes:
//  1. Queued — channel had room.
//  2. Stopped — Stop() already called; item silently dropped (no panic).
//  3. Full — channel buffer full; item dropped with slog.Warn.
func (w *IndexingWorker) Enqueue(out store.ToolOutput) {
	select {
	case <-w.stopped:
		// Worker is stopping/stopped — drop silently, no panic.
		return
	default:
	}

	select {
	case w.ch <- out:
		// queued
	case <-w.stopped:
		// Raced with Stop() — drop silently.
	default:
		slog.Warn("indexing_worker: channel full, dropping output",
			"tool", out.ToolName,
			"id", out.ID,
		)
	}
}

// Stop closes the input channel and waits up to 2 s for the drain to complete.
// Safe to call multiple times (idempotent via sync.Once).
func (w *IndexingWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopped) // signal Enqueue to stop racing
		close(w.ch)      // signal loop to exit after draining
	})
	select {
	case <-w.closed:
		// drained cleanly
	case <-time.After(2 * time.Second):
		slog.Warn("indexing_worker: drain timed out after 2s")
	}
}
