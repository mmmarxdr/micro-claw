package agent

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/store"

	_ "modernc.org/sqlite"
)

// ─── normalizeEmbedding tests ─────────────────────────────────────────────────

func TestNormalizeEmbedding_TruncatesLargerTo256(t *testing.T) {
	input := make([]float32, 1536)
	for i := range input {
		input[i] = float32(i)
	}
	got := normalizeEmbedding(input, 256)
	if len(got) != 256 {
		t.Errorf("expected len=256 after truncate, got %d", len(got))
	}
	// First element must match.
	if got[0] != input[0] {
		t.Errorf("first element mismatch: got %v, want %v", got[0], input[0])
	}
	// Last element of truncated must be index 255.
	if got[255] != input[255] {
		t.Errorf("element at index 255: got %v, want %v", got[255], input[255])
	}
}

func TestNormalizeEmbedding_PadsSmallerTo256(t *testing.T) {
	input := make([]float32, 64)
	for i := range input {
		input[i] = float32(i + 1)
	}
	got := normalizeEmbedding(input, 256)
	if len(got) != 256 {
		t.Errorf("expected len=256 after pad, got %d", len(got))
	}
	// Original values preserved.
	for i, v := range input {
		if got[i] != v {
			t.Errorf("element[%d]: got %v, want %v", i, got[i], v)
		}
	}
	// Padded positions must be zero.
	for i := len(input); i < 256; i++ {
		if got[i] != 0 {
			t.Errorf("padded element[%d]: got %v, want 0.0", i, got[i])
		}
	}
}

func TestNormalizeEmbedding_ExactlyReturnsUnchanged(t *testing.T) {
	input := make([]float32, 256)
	for i := range input {
		input[i] = float32(i) * 0.5
	}
	got := normalizeEmbedding(input, 256)
	if len(got) != 256 {
		t.Errorf("expected len=256, got %d", len(got))
	}
	for i, v := range input {
		if got[i] != v {
			t.Errorf("element[%d]: got %v, want %v", i, got[i], v)
		}
	}
}

// ─── serialize + deserialize roundtrip tests ──────────────────────────────────

func TestSerializeDeserializeEmbedding_Roundtrip(t *testing.T) {
	input := make([]float32, 256)
	for i := range input {
		input[i] = float32(i)*0.01 + 0.001
	}

	blob := serializeEmbedding(input)
	if len(blob) != 1024 {
		t.Errorf("expected BLOB size 1024, got %d", len(blob))
	}

	got := deserializeEmbedding(blob)
	if len(got) != 256 {
		t.Errorf("expected 256 floats after deserialize, got %d", len(got))
	}
	for i, v := range input {
		if math.Abs(float64(got[i]-v)) > 1e-7 {
			t.Errorf("element[%d]: got %v, want %v", i, got[i], v)
		}
	}
}

func TestSerializeEmbedding_1536DimTruncatedBlobIs1024Bytes(t *testing.T) {
	// Scenario 13: 1536-dim input → normalize to 256 → BLOB = 1024 bytes.
	input := make([]float32, 1536)
	for i := range input {
		input[i] = float32(i) * 0.001
	}
	normalized := normalizeEmbedding(input, 256)
	blob := serializeEmbedding(normalized)
	if len(blob) != 1024 {
		t.Errorf("1536→256 truncation: BLOB size = %d, want 1024", len(blob))
	}
}

func TestSerializeEmbedding_ZeroPaddedBytes(t *testing.T) {
	// Padded zeros must serialize as exactly 0x00 bytes.
	input := make([]float32, 10)
	input[0] = 1.0
	normalized := normalizeEmbedding(input, 256)
	blob := serializeEmbedding(normalized)

	// Bytes for indices 10..255 (each 4 bytes) must all be 0x00.
	for i := 40; i < 1024; i++ {
		if blob[i] != 0 {
			t.Errorf("padded byte[%d] = %d, want 0", i, blob[i])
		}
	}
}

// ─── EmbeddingWorker tests ────────────────────────────────────────────────────

// mockEmbeddingProvider implements provider.EmbeddingProvider.
type mockEmbeddingProvider struct {
	embedFn func(ctx context.Context, text string) ([]float32, error)
	calls   int
}

func (m *mockEmbeddingProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	m.calls++
	return m.embedFn(ctx, text)
}

// openTestDB opens an in-memory SQLite database and runs the store migrations.
// Returns the *sql.DB for direct access.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := t.TempDir()
	s, err := store.NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	db := s.DB()
	t.Cleanup(func() {
		s.Close()
	})
	return db
}

// insertTestMemory inserts a minimal memory entry into the DB and returns its ID.
func insertTestMemory(t *testing.T, db *sql.DB, id, scopeID, content string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO memory (id, scope_id, content, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, scopeID, content, "test", time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("insertTestMemory: %v", err)
	}
}

func TestEmbeddingWorker_HappyPath(t *testing.T) {
	db := openTestDB(t)
	insertTestMemory(t, db, "entry-1", "scope1", "hello world")

	embeddings := make([]float32, 256)
	for i := range embeddings {
		embeddings[i] = float32(i) * 0.01
	}

	prov := &mockEmbeddingProvider{
		embedFn: func(ctx context.Context, text string) ([]float32, error) {
			return embeddings, nil
		},
	}

	w := NewEmbeddingWorker(prov, db, config.StoreConfig{Embeddings: true})
	if w == nil {
		t.Fatal("expected non-nil EmbeddingWorker")
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()

	w.Enqueue("entry-1", "scope1", "hello world")

	// Give worker time to process.
	time.Sleep(200 * time.Millisecond)

	// Verify the embedding BLOB was written to the DB.
	var blob []byte
	err := db.QueryRow(`SELECT embedding FROM memory WHERE id = 'entry-1'`).Scan(&blob)
	if err != nil {
		t.Fatalf("reading embedding: %v", err)
	}
	if len(blob) != 1024 {
		t.Errorf("expected BLOB size 1024, got %d", len(blob))
	}
	_ = cancel
}

func TestEmbeddingWorker_ChannelFullDrops(t *testing.T) {
	db := openTestDB(t)

	slow := make(chan struct{})
	callCount := 0
	prov := &mockEmbeddingProvider{
		embedFn: func(ctx context.Context, text string) ([]float32, error) {
			callCount++
			<-slow // block until released
			return make([]float32, 256), nil
		},
	}

	w := NewEmbeddingWorker(prov, db, config.StoreConfig{Embeddings: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Fill the channel beyond capacity (cap=5) — extras should be dropped without blocking.
	for i := 0; i < 10; i++ {
		id := "entry-drop"
		insertTestMemory(t, db, id+string(rune('0'+i)), "scope1", "text")
		w.Enqueue(id, "scope1", "text")
	}

	// Unblock the worker.
	close(slow)
	w.Stop()

	// No panic means the non-blocking select worked correctly.
}

func TestEmbeddingWorker_NilWhenEmbeddingsDisabled(t *testing.T) {
	db := openTestDB(t)
	prov := &mockEmbeddingProvider{
		embedFn: func(_ context.Context, _ string) ([]float32, error) {
			return make([]float32, 256), nil
		},
	}
	w := NewEmbeddingWorker(prov, db, config.StoreConfig{Embeddings: false})
	if w != nil {
		t.Error("expected nil EmbeddingWorker when Embeddings=false")
	}
}

func TestEmbeddingWorker_NilWhenProviderLacksEmbedInterface(t *testing.T) {
	db := openTestDB(t)
	// Pass a plain non-embedding provider (nil implements no interface).
	w := NewEmbeddingWorker(nil, db, config.StoreConfig{Embeddings: true})
	if w != nil {
		t.Error("expected nil EmbeddingWorker when provider doesn't implement EmbeddingProvider")
	}
}
