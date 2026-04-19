package rag_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"daimon/internal/rag"
)

// mockMediaStore implements rag.MediaStoreReader for testing.
type mockMediaStore struct {
	mu      sync.Mutex
	media   map[string][]byte
	mimes   map[string]string
	getCalls int
}

func newMockMediaStore() *mockMediaStore {
	return &mockMediaStore{
		media: make(map[string][]byte),
		mimes: make(map[string]string),
	}
}

func (m *mockMediaStore) GetMedia(_ context.Context, sha256 string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	data, ok := m.media[sha256]
	if !ok {
		return nil, "", rag.ErrDocNotFound
	}
	return data, m.mimes[sha256], nil
}

// trackingStore wraps mockDocumentStore but records calls.
type trackingStore struct {
	mu       sync.Mutex
	docs     []rag.Document
	chunks   map[string][]rag.DocumentChunk
}

func newTrackingStore() *trackingStore {
	return &trackingStore{chunks: make(map[string][]rag.DocumentChunk)}
}

func (s *trackingStore) AddDocument(_ context.Context, doc rag.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs = append(s.docs, doc)
	return nil
}

func (s *trackingStore) AddChunks(_ context.Context, docID string, chunks []rag.DocumentChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks[docID] = append(s.chunks[docID], chunks...)
	return nil
}

func (s *trackingStore) SearchChunks(_ context.Context, _ string, _ []float32, _ int) ([]rag.SearchResult, error) {
	return nil, nil
}
func (s *trackingStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (s *trackingStore) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}

// T4.1 — DocIngestionWorker

func TestDocIngestionWorker_InlineText(t *testing.T) {
	store := newTrackingStore()
	media := newMockMediaStore()
	embedFn := func(_ context.Context, text string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		EmbedFn:    embedFn,
		MediaStore: media,
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	job := rag.IngestionJob{
		DocID:     "doc-001",
		Namespace: "global",
		Title:     "Test Doc",
		Content:   "This is the inline content for the document.",
		MIME:      "text/plain",
	}

	w.Enqueue(job)

	// Wait for processing with timeout
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		docCount := len(store.docs)
		store.mu.Unlock()
		if docCount > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	w.Stop()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.docs) == 0 {
		t.Fatal("expected at least one document to be stored")
	}
	if len(store.chunks["doc-001"]) == 0 {
		t.Error("expected chunks to be stored for doc-001")
	}
}

func TestDocIngestionWorker_SHA256FetchAndExtract(t *testing.T) {
	store := newTrackingStore()
	media := newMockMediaStore()

	media.mu.Lock()
	media.media["abc123"] = []byte("Content fetched from media store.")
	media.mimes["abc123"] = "text/plain"
	media.mu.Unlock()

	embedFn := func(_ context.Context, text string) ([]float32, error) {
		return []float32{0.5, 0.5}, nil
	}

	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		EmbedFn:    embedFn,
		MediaStore: media,
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	job := rag.IngestionJob{
		DocID:     "doc-sha",
		Namespace: "global",
		Title:     "SHA Doc",
		SHA256:    "abc123",
		MIME:      "text/plain",
	}
	w.Enqueue(job)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		docCount := len(store.docs)
		store.mu.Unlock()
		if docCount > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	w.Stop()

	media.mu.Lock()
	getCalls := media.getCalls
	media.mu.Unlock()
	if getCalls == 0 {
		t.Error("expected GetMedia to be called for SHA256 job")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.docs) == 0 {
		t.Fatal("expected document to be stored")
	}
}

func TestDocIngestionWorker_StopIdempotent(t *testing.T) {
	store := newTrackingStore()
	media := newMockMediaStore()

	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		MediaStore: media,
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	ctx := context.Background()
	w.Start(ctx)

	// Stop multiple times — should not panic or deadlock
	done := make(chan struct{})
	go func() {
		w.Stop()
		w.Stop()
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// pass
	case <-time.After(3 * time.Second):
		t.Error("Stop() timed out — possible deadlock")
	}
}

func TestDocIngestionWorker_FullChannelDrops(t *testing.T) {
	store := newTrackingStore()
	media := newMockMediaStore()

	w := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      store,
		Extractor:  rag.NewSelectExtractor(rag.PlainTextExtractor{}),
		Chunker:    rag.FixedSizeChunker{},
		MediaStore: media,
		ChunkOpts:  rag.ChunkOptions{Size: 512, Overlap: 64},
	})

	// Do NOT start the worker — channel will fill up quickly.
	// Enqueue more than cap(5) jobs; the extras should be dropped without blocking.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			w.Enqueue(rag.IngestionJob{
				DocID:   "doc",
				Content: "text",
				MIME:    "text/plain",
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// All enqueues returned without blocking — pass
	case <-time.After(2 * time.Second):
		t.Error("Enqueue blocked when channel is full — should drop")
	}
}
