package agent

import (
	"context"
	"database/sql"
	"encoding/binary"
	"log/slog"
	"math"
	"sync"
	"time"

	"microagent/internal/config"
	"microagent/internal/provider"
)

// embeddingDims is the number of float32 dimensions stored per entry.
// All provider outputs are normalized to this size before storage.
const embeddingDims = 256

// normalizeEmbedding truncates or zero-pads vec to exactly dims dimensions.
// If len(vec) >= dims, the first dims elements are returned (truncate).
// If len(vec) < dims, the vector is zero-padded to dims (pad).
func normalizeEmbedding(vec []float32, dims int) []float32 {
	if len(vec) >= dims {
		return vec[:dims]
	}
	padded := make([]float32, dims)
	copy(padded, vec)
	return padded
}

// serializeEmbedding converts a float32 slice to a little-endian binary BLOB.
// Each float32 occupies 4 bytes, so a 256-dim vector produces 1024 bytes.
func serializeEmbedding(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// deserializeEmbedding converts a little-endian binary BLOB back to a float32 slice.
// len(data) must be a multiple of 4; any trailing bytes are ignored.
func deserializeEmbedding(data []byte) []float32 {
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// ─── EmbeddingWorker ─────────────────────────────────────────────────────────

// embedJob represents a single embedding request queued for async processing.
type embedJob struct {
	entryID string
	scopeID string
	content string
}

// EmbeddingWorker asynchronously generates embeddings for new memory entries and
// stores them directly in the SQLite database. It runs a single worker goroutine
// draining a bounded channel.
//
// Use NewEmbeddingWorker to construct — it returns nil when embeddings are disabled
// or the provider does not implement EmbeddingProvider.
type EmbeddingWorker struct {
	db      *sql.DB
	embedder provider.EmbeddingProvider
	ch       chan embedJob
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

// NewEmbeddingWorker constructs an EmbeddingWorker.
// Returns nil (not an error) in two cases:
//  1. cfg.Embeddings is false — embeddings are disabled by configuration.
//  2. embProvider is nil or does not implement provider.EmbeddingProvider — WARN is logged.
//
// The worker is not started here; call Start(ctx) to begin processing.
func NewEmbeddingWorker(embProvider any, db *sql.DB, cfg config.StoreConfig) *EmbeddingWorker {
	if !cfg.Embeddings {
		return nil
	}

	ep, ok := embProvider.(provider.EmbeddingProvider)
	if !ok || embProvider == nil {
		slog.Warn("embeddings enabled in config but provider does not implement EmbeddingProvider; embedding disabled")
		return nil
	}

	return &EmbeddingWorker{
		db:       db,
		embedder: ep,
		ch:       make(chan embedJob, 5),
	}
}

// Start begins the background worker goroutine. It is driven by ctx — when ctx
// is cancelled the worker drains remaining queued jobs and exits cleanly.
func (w *EmbeddingWorker) Start(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go w.run(workerCtx)
}

// Enqueue submits a job for embedding. Non-blocking: if the internal channel is
// full, the job is silently dropped and a DEBUG log is emitted.
func (w *EmbeddingWorker) Enqueue(entryID, scopeID, content string) {
	select {
	case w.ch <- embedJob{entryID: entryID, scopeID: scopeID, content: content}:
	default:
		slog.Debug("embedding worker channel full, dropping job", "entry_id", entryID)
	}
}

// Stop signals the worker to stop and waits for it to exit.
func (w *EmbeddingWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// run is the worker loop. It processes jobs until ctx is cancelled and the
// channel is drained.
func (w *EmbeddingWorker) run(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Drain remaining jobs before exiting.
			// Use a 2-second timeout per job to avoid blocking Stop() for too long
			// (channel capacity is 5, so worst case is 10s total without this limit).
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer drainCancel()
		drain:
			for {
				select {
				case job := <-w.ch:
					w.processJob(drainCtx, job)
				default:
					break drain
				}
			}
			return
		case job := <-w.ch:
			w.processJob(ctx, job)
		}
	}
}

// processJob calls the embedding provider and stores the result in the DB.
// A 10-second per-call timeout is applied. Failures are logged at WARN level
// and never propagated — embedding is best-effort.
func (w *EmbeddingWorker) processJob(ctx context.Context, job embedJob) {
	callCtx, cancel := context.WithTimeout(ctx, 10_000_000_000) // 10s
	defer cancel()

	vec, err := w.embedder.Embed(callCtx, job.content)
	if err != nil {
		slog.Warn("embedding worker: Embed failed", "entry_id", job.entryID, "error", err)
		return
	}

	normalized := normalizeEmbedding(vec, embeddingDims)
	blob := serializeEmbedding(normalized)

	_, err = w.db.ExecContext(ctx,
		`UPDATE memory SET embedding = ? WHERE id = ? AND scope_id = ?`,
		blob, job.entryID, job.scopeID,
	)
	if err != nil {
		slog.Warn("embedding worker: DB update failed", "entry_id", job.entryID, "error", err)
	}
}
