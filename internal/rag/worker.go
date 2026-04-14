package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// MediaStoreReader is a minimal interface to read blobs from a media store.
// It avoids importing the full store package and breaks import cycles.
type MediaStoreReader interface {
	GetMedia(ctx context.Context, sha256 string) ([]byte, string, error)
}

// IngestionJob represents a single document queued for ingestion.
type IngestionJob struct {
	DocID     string // unique document identifier
	Namespace string // scoping namespace (e.g. "global")
	Title     string // human-readable document title
	Content   string // inline text content (used when SHA256 is empty)
	SHA256    string // MediaStore blob reference (used when Content is empty)
	MIME      string // MIME type of the content
}

// DocIngestionWorkerConfig holds all dependencies for DocIngestionWorker.
type DocIngestionWorkerConfig struct {
	Store      DocumentStore
	Extractor  Extractor
	Chunker    Chunker
	EmbedFn    func(ctx context.Context, text string) ([]float32, error) // nil = no embedding
	MediaStore MediaStoreReader
	ChunkOpts  ChunkOptions
}

// DocIngestionWorker asynchronously ingests documents into the RAG store.
// It extracts text, chunks it, optionally embeds each chunk, then persists.
//
// Use NewDocIngestionWorker to construct; call Start(ctx) before Enqueue.
type DocIngestionWorker struct {
	ch         chan IngestionJob
	wg         sync.WaitGroup
	stopOnce   sync.Once
	cancel     context.CancelFunc
	store      DocumentStore
	extractor  Extractor
	chunker    Chunker
	embedFn    func(ctx context.Context, text string) ([]float32, error)
	mediaStore MediaStoreReader
	chunkOpts  ChunkOptions
}

// NewDocIngestionWorker constructs a DocIngestionWorker from the given config.
// The worker is not started; call Start(ctx) to begin processing.
func NewDocIngestionWorker(cfg DocIngestionWorkerConfig) *DocIngestionWorker {
	return &DocIngestionWorker{
		ch:         make(chan IngestionJob, 5),
		store:      cfg.Store,
		extractor:  cfg.Extractor,
		chunker:    cfg.Chunker,
		embedFn:    cfg.EmbedFn,
		mediaStore: cfg.MediaStore,
		chunkOpts:  cfg.ChunkOpts,
	}
}

// Start begins the background worker goroutine. When ctx is cancelled,
// the worker drains remaining queued jobs and exits cleanly.
func (w *DocIngestionWorker) Start(ctx context.Context) {
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go w.run(workerCtx)
}

// Enqueue submits a job for ingestion. Non-blocking: if the internal channel
// is full, the job is silently dropped and a DEBUG log is emitted.
func (w *DocIngestionWorker) Enqueue(job IngestionJob) {
	select {
	case w.ch <- job:
	default:
		slog.Debug("rag: ingestion worker channel full, dropping job", "doc_id", job.DocID)
	}
}

// Stop signals the worker to stop and waits for it to finish. Idempotent.
func (w *DocIngestionWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	w.wg.Wait()
}

// run is the main worker loop. Processes jobs until ctx is cancelled, then drains.
func (w *DocIngestionWorker) run(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Drain remaining jobs with a short deadline.
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

// processJob executes the full ingestion pipeline for a single job.
func (w *DocIngestionWorker) processJob(ctx context.Context, job IngestionJob) {
	text, err := w.resolveText(ctx, job)
	if err != nil {
		slog.Warn("rag: failed to resolve text for job", "doc_id", job.DocID, "error", err)
		return
	}

	// Chunk the text.
	rawChunks := w.chunker.Chunk(text, w.chunkOpts)

	// Embed each chunk (best-effort).
	chunks := make([]DocumentChunk, 0, len(rawChunks))
	for _, ch := range rawChunks {
		ch.DocID = job.DocID
		ch.ID = fmt.Sprintf("%s/%s", job.DocID, ch.ID)

		if w.embedFn != nil {
			embedCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			vec, embedErr := w.embedFn(embedCtx, ch.Content)
			cancel()
			if embedErr != nil {
				slog.Warn("rag: embed failed for chunk", "chunk_id", ch.ID, "error", embedErr)
			} else {
				ch.Embedding = NormalizeEmbedding(vec, 256)
			}
		}
		chunks = append(chunks, ch)
	}

	// Persist document.
	doc := Document{
		ID:           job.DocID,
		Namespace:    job.Namespace,
		Title:        job.Title,
		SourceSHA256: job.SHA256,
		MIME:         job.MIME,
		ChunkCount:   len(chunks),
	}
	if err := w.store.AddDocument(ctx, doc); err != nil {
		slog.Warn("rag: failed to add document", "doc_id", job.DocID, "error", err)
		return
	}

	if len(chunks) > 0 {
		if err := w.store.AddChunks(ctx, job.DocID, chunks); err != nil {
			slog.Warn("rag: failed to add chunks", "doc_id", job.DocID, "error", err)
		}
	}
}

// resolveText returns the text for the job: either inline Content or fetched+extracted from MediaStore.
func (w *DocIngestionWorker) resolveText(ctx context.Context, job IngestionJob) (string, error) {
	if job.SHA256 != "" {
		if w.mediaStore == nil {
			return "", fmt.Errorf("rag: SHA256 job requires a MediaStoreReader")
		}
		data, mime, err := w.mediaStore.GetMedia(ctx, job.SHA256)
		if err != nil {
			return "", fmt.Errorf("rag: GetMedia(%s): %w", job.SHA256, err)
		}
		// Use provided MIME if media store returns empty.
		if mime == "" {
			mime = job.MIME
		}
		if w.extractor != nil && w.extractor.Supports(mime) {
			doc, err := w.extractor.Extract(ctx, data, mime)
			if err != nil {
				return "", fmt.Errorf("rag: extract from SHA256 %s: %w", job.SHA256, err)
			}
			return doc.Text, nil
		}
		// Fall back to raw bytes as text.
		return string(data), nil
	}

	// Inline content path — optionally extract if extractor can handle the MIME.
	if w.extractor != nil && job.MIME != "" && w.extractor.Supports(job.MIME) {
		doc, err := w.extractor.Extract(ctx, []byte(job.Content), job.MIME)
		if err != nil {
			return "", fmt.Errorf("rag: extract inline content: %w", err)
		}
		return doc.Text, nil
	}

	return job.Content, nil
}
