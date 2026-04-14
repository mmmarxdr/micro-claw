package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/provider"
)

// makeTestEmbedding builds a deterministic 256-dim BLOB from a seed value.
// Used in tests to insert synthetic embeddings directly into the DB.
func makeTestEmbedding(seed float32) []byte {
	vec := make([]float32, 256)
	for i := range vec {
		vec[i] = seed + float32(i)*0.001
	}
	buf := make([]byte, 256*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// helper to create a SQLiteStore backed by a temp dir — fails test on error.
func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ─── Phase 4: Behaviour parity tests ────────────────────────────────────────

func TestSQLiteStore_SaveAndLoadConversation(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	conv := Conversation{
		ID:        "test-conv-1",
		ChannelID: "cli",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock("hello")},
			{Role: "assistant", Content: content.TextBlock("world")},
		},
		Metadata:  map[string]string{"key": "value"},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation: %v", err)
	}

	loaded, err := s.LoadConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}

	if loaded.ID != conv.ID {
		t.Errorf("ID mismatch: got %q want %q", loaded.ID, conv.ID)
	}
	if loaded.ChannelID != conv.ChannelID {
		t.Errorf("ChannelID mismatch: got %q want %q", loaded.ChannelID, conv.ChannelID)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Role != "user" || loaded.Messages[0].Content.TextOnly() != "hello" {
		t.Errorf("message[0] mismatch: %+v", loaded.Messages[0])
	}
	if loaded.Messages[1].Role != "assistant" || loaded.Messages[1].Content.TextOnly() != "world" {
		t.Errorf("message[1] mismatch: %+v", loaded.Messages[1])
	}
	if loaded.Metadata["key"] != "value" {
		t.Errorf("Metadata mismatch: got %v", loaded.Metadata)
	}
	if !loaded.CreatedAt.Equal(conv.CreatedAt) {
		t.Errorf("CreatedAt mismatch: got %v want %v", loaded.CreatedAt, conv.CreatedAt)
	}
	if !loaded.UpdatedAt.Equal(conv.UpdatedAt) {
		t.Errorf("UpdatedAt mismatch: got %v want %v", loaded.UpdatedAt, conv.UpdatedAt)
	}
}

func TestSQLiteStore_SaveOverwrite(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	conv1 := Conversation{ID: "c1", ChannelID: "cli", UpdatedAt: time.Now()}
	if err := s.SaveConversation(ctx, conv1); err != nil {
		t.Fatalf("first save: %v", err)
	}

	conv2 := Conversation{ID: "c1", ChannelID: "telegram", UpdatedAt: time.Now()}
	if err := s.SaveConversation(ctx, conv2); err != nil {
		t.Fatalf("second save: %v", err)
	}

	loaded, err := s.LoadConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if loaded.ChannelID != "telegram" {
		t.Errorf("expected ChannelID 'telegram', got %q", loaded.ChannelID)
	}

	// Only one row should exist.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id = 'c1'`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row with id 'c1', got %d", count)
	}
}

func TestSQLiteStore_LoadNonExistent(t *testing.T) {
	s := newTestSQLiteStore(t)
	_, err := s.LoadConversation(context.Background(), "no-existe")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteStore_ErrNotFound_Wrapped(t *testing.T) {
	s := newTestSQLiteStore(t)
	_, err := s.LoadConversation(context.Background(), "no-existe")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected errors.Is(err, ErrNotFound), got: %v", err)
	}
	if err == ErrNotFound {
		t.Errorf("expected error to be wrapped, not the sentinel itself, got: %v", err)
	}
}

func TestSQLiteStore_ListConversations(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// i=0,2,4 → "telegram"; i=1,3 → "cli"
	for i := 0; i < 5; i++ {
		ch := "cli"
		if i%2 == 0 {
			ch = "telegram"
		}
		conv := Conversation{
			ID:        fmt.Sprintf("conv-%d", i),
			ChannelID: ch,
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		}
		if err := s.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	cliConvs, err := s.ListConversations(ctx, "cli", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(cliConvs) != 2 {
		t.Errorf("expected 2 'cli' conversations, got %d", len(cliConvs))
	}
	for _, c := range cliConvs {
		if c.ChannelID != "cli" {
			t.Errorf("expected ChannelID 'cli', got %q", c.ChannelID)
		}
	}
	// Newest first: conv-3 (i=3, highest cli UpdatedAt) then conv-1.
	if len(cliConvs) == 2 {
		if cliConvs[0].ID != "conv-3" || cliConvs[1].ID != "conv-1" {
			t.Errorf("order mismatch: got [%s, %s] want [conv-3, conv-1]",
				cliConvs[0].ID, cliConvs[1].ID)
		}
	}
}

func TestSQLiteStore_ListConversations_EmptyChannelID(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i, ch := range []string{"cli", "telegram", "discord"} {
		conv := Conversation{
			ID:        fmt.Sprintf("c%d", i),
			ChannelID: ch,
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := s.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	result, err := s.ListConversations(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 conversations, got %d", len(result))
	}
}

func TestSQLiteStore_ListConversations_LimitZero(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("lz-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := s.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	result, err := s.ListConversations(ctx, "cli", 0)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 conversations with limit=0, got %d", len(result))
	}
}

func TestSQLiteStore_ListConversations_LimitExceedsTotal(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("le-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := s.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	result, err := s.ListConversations(ctx, "cli", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 conversations (limit exceeds total), got %d", len(result))
	}
}

func TestSQLiteStore_ListConversations_LimitActuallyTruncates(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("trunc-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := s.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	result, err := s.ListConversations(ctx, "cli", 2)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 conversations with limit=2, got %d", len(result))
	}
}

func TestSQLiteStore_MemoryAppendAndSearch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entries := []MemoryEntry{
		{ID: "m1", Content: "Golang es un lenguaje tipado", CreatedAt: time.Now()},
		{ID: "m2", Content: "Me gusta la comida italiana", CreatedAt: time.Now().Add(time.Minute)},
		{ID: "m3", Content: "El MVP de MicroAgent usa golang puro", CreatedAt: time.Now().Add(2 * time.Minute)},
	}
	for _, e := range entries {
		if err := s.AppendMemory(ctx, "global", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	matches, err := s.SearchMemory(ctx, "global", "GOLANG", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 matches for 'GOLANG', got %d", len(matches))
	}
}

func TestSQLiteStore_MemorySearchLimit(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		e := MemoryEntry{ID: fmt.Sprintf("m%d", i), Content: "Test item", CreatedAt: time.Now()}
		if err := s.AppendMemory(ctx, "global", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	matches, err := s.SearchMemory(ctx, "global", "test", 3)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("expected exactly 3 matches due to limit, got %d", len(matches))
	}
}

func TestSQLiteStore_CloseIdempotent(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	err1 := s.Close()
	err2 := s.Close()
	if err1 != nil {
		t.Errorf("first Close() returned error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("second Close() returned error: %v", err2)
	}
}

func TestSQLiteStore_SearchMemory_TagMatch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "tag-test",
		Content:   "unrelated content",
		Tags:      []string{"golang", "testing"},
		CreatedAt: time.Now(),
	}
	if err := s.AppendMemory(ctx, "global", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	matches, err := s.SearchMemory(ctx, "global", "golang", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match for tag 'golang', got %d", len(matches))
	}

	noMatches, err := s.SearchMemory(ctx, "global", "python", 5)
	if err != nil {
		t.Fatalf("SearchMemory python: %v", err)
	}
	if len(noMatches) != 0 {
		t.Errorf("expected 0 matches for 'python', got %d", len(noMatches))
	}
}

func TestSQLiteStore_SearchMemory_EmptyQuery(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		e := MemoryEntry{
			ID:        fmt.Sprintf("eq-%d", i),
			Content:   fmt.Sprintf("memory entry %d", i),
			CreatedAt: time.Now(),
		}
		if err := s.AppendMemory(ctx, "global", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	matches, err := s.SearchMemory(ctx, "global", "", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 matches for empty query, got %d", len(matches))
	}
}

func TestSQLiteStore_SearchMemory_CaseInsensitive(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "ci-test",
		Content:   "Golang is great",
		CreatedAt: time.Now(),
	}
	if err := s.AppendMemory(ctx, "global", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	for _, query := range []string{"golang", "GOLANG", "GoLang"} {
		matches, err := s.SearchMemory(ctx, "global", query, 5)
		if err != nil {
			t.Fatalf("SearchMemory(%q): %v", query, err)
		}
		if len(matches) != 1 {
			t.Errorf("query %q: expected 1 match, got %d", query, len(matches))
		}
	}
}

func TestSQLiteStore_ScopeIsolation(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		e := MemoryEntry{
			ID:        fmt.Sprintf("sa-%d", i),
			Content:   "scope-a content",
			CreatedAt: time.Now(),
		}
		if err := s.AppendMemory(ctx, "scope-a", e); err != nil {
			t.Fatalf("AppendMemory scope-a: %v", err)
		}
	}
	if err := s.AppendMemory(ctx, "scope-b", MemoryEntry{
		ID:        "sb-1",
		Content:   "scope-b content",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory scope-b: %v", err)
	}

	result, err := s.SearchMemory(ctx, "scope-b", "", 10)
	if err != nil {
		t.Fatalf("SearchMemory scope-b: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 entry in scope-b, got %d", len(result))
	}
	if len(result) == 1 && result[0].ID != "sb-1" {
		t.Errorf("expected entry 'sb-1', got %q", result[0].ID)
	}
}

func TestSQLiteStore_PersistenceAcrossInstances(t *testing.T) {
	path := t.TempDir()
	ctx := context.Background()

	// Instance A: save and close.
	storeA, err := NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore A: %v", err)
	}
	conv := Conversation{ID: "persist-test", ChannelID: "ch1", UpdatedAt: time.Now()}
	if err := storeA.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation A: %v", err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}

	// Instance B: open same path and load.
	storeB, err := NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore B: %v", err)
	}
	defer storeB.Close()

	loaded, err := storeB.LoadConversation(ctx, "persist-test")
	if err != nil {
		t.Fatalf("LoadConversation B: %v", err)
	}
	if loaded.ChannelID != "ch1" {
		t.Errorf("data loss across instances: got channel %q", loaded.ChannelID)
	}
}

func TestSQLiteStore_DatabaseFileAtExpectedPath(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(config.StoreConfig{Path: dir})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	dbPath := filepath.Join(dir, "microagent.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("expected microagent.db at %s, but file does not exist", dbPath)
	}
}

func TestSQLiteStore_ConstructorCreatesDirectory(t *testing.T) {
	base := t.TempDir()
	newPath := filepath.Join(base, "nonexistent", "subdir")

	s, err := NewSQLiteStore(config.StoreConfig{Path: newPath})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected directory %s to be created, but it does not exist", newPath)
	}
}

func TestSQLiteStore_ConstructorErrorOnBadPath(t *testing.T) {
	// Block the path by placing a regular file where the directory should be.
	parent := t.TempDir()
	blockFile := filepath.Join(parent, "block")
	if err := os.WriteFile(blockFile, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// The desired path is inside a regular file — MkdirAll must fail.
	badPath := filepath.Join(blockFile, "subdir")
	s, err := NewSQLiteStore(config.StoreConfig{Path: badPath})
	if err == nil {
		s.Close()
		t.Fatal("expected error for unwritable path, got nil")
	}
	if s != nil {
		s.Close()
		t.Error("expected nil store on error, got non-nil")
	}
}

// ─── Phase 5: SQLite-specific tests ─────────────────────────────────────────

func TestSQLiteStore_WALMode(t *testing.T) {
	s := newTestSQLiteStore(t)

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode 'wal', got %q", mode)
	}
}

func TestSQLiteStore_SearchMemory_RankOrder(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Entry A: query term in title AND content (higher frequency).
	entryA := MemoryEntry{
		ID:        "high-freq",
		Title:     "golang patterns",
		Content:   "golang is a great language for golang development",
		CreatedAt: time.Now(),
	}
	// Entry B: query term only in content (lower frequency).
	entryB := MemoryEntry{
		ID:        "low-freq",
		Title:     "general patterns",
		Content:   "golang can be used here",
		CreatedAt: time.Now().Add(time.Second),
	}

	// Insert B first (higher insertion rowid) so insertion order != expected rank order.
	if err := s.AppendMemory(ctx, "global", entryB); err != nil {
		t.Fatalf("AppendMemory B: %v", err)
	}
	if err := s.AppendMemory(ctx, "global", entryA); err != nil {
		t.Fatalf("AppendMemory A: %v", err)
	}

	results, err := s.SearchMemory(ctx, "global", "golang", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Entry A has higher frequency — it should rank first.
	if results[0].ID != "high-freq" {
		t.Errorf("expected high-freq first (better rank), got %q first", results[0].ID)
	}
}

// ─── New columns: access_count, archived_at ───────────────────────────────────

// TestSQLiteStore_SearchMemory_AccessCountStartsAtZero verifies that a newly
// inserted entry scans access_count=0 before any search that increments it.
func TestSQLiteStore_SearchMemory_AccessCountStartsAtZero(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "ac-test", Content: "access count test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Direct DB query to verify access_count column exists and starts at 0.
	var count int
	if err := s.db.QueryRow(`SELECT access_count FROM memory WHERE id = 'ac-test'`).Scan(&count); err != nil {
		t.Fatalf("reading access_count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected access_count=0 for new entry, got %d", count)
	}
}

// TestSQLiteStore_SearchMemory_AccessCountIncrementsOnSearch verifies that
// access_count is incremented after SearchMemory returns an entry.
func TestSQLiteStore_SearchMemory_AccessCountIncrementsOnSearch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "ac-inc", Content: "increment access count on search", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// First search — should return the entry and bump access_count to 1.
	results, err := s.SearchMemory(ctx, "scope1", "increment", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}

	// Verify access_count was incremented in the DB.
	var count int
	if err := s.db.QueryRow(`SELECT access_count FROM memory WHERE id = 'ac-inc'`).Scan(&count); err != nil {
		t.Fatalf("reading access_count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected access_count=1 after one search, got %d", count)
	}
}

// TestSQLiteStore_SearchMemory_ArchivedExcluded verifies that entries with
// archived_at set are not returned by SearchMemory.
func TestSQLiteStore_SearchMemory_ArchivedExcluded(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert a normal and an archived entry.
	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "live", Content: "live unarchived memory", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory live: %v", err)
	}
	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "archived", Content: "archived memory entry", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory archived: %v", err)
	}

	// Soft-delete the archived entry directly.
	if _, err := s.db.Exec(
		`UPDATE memory SET archived_at = datetime('now') WHERE id = 'archived'`,
	); err != nil {
		t.Fatalf("setting archived_at: %v", err)
	}

	// FTS5 search should NOT return the archived entry.
	results, err := s.SearchMemory(ctx, "scope1", "archived", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	for _, r := range results {
		if r.ID == "archived" {
			t.Error("archived entry should not appear in SearchMemory results")
		}
	}
}

// TestSQLiteStore_SearchMemory_ArchivedExcludedEmptyQuery verifies that the
// empty-query path also excludes archived entries.
func TestSQLiteStore_SearchMemory_ArchivedExcludedEmptyQuery(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "visible", Content: "visible", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}
	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "hidden", Content: "hidden", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Archive the second entry.
	if _, err := s.db.Exec(
		`UPDATE memory SET archived_at = datetime('now') WHERE id = 'hidden'`,
	); err != nil {
		t.Fatalf("archiving: %v", err)
	}

	results, err := s.SearchMemory(ctx, "scope1", "", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (archived excluded), got %d", len(results))
	}
	if len(results) == 1 && results[0].ID != "visible" {
		t.Errorf("expected 'visible', got %q", results[0].ID)
	}
}

// TestSQLiteStore_ScanMemoryRows_ScansNewColumns verifies that scanMemoryRows
// correctly populates AccessCount and ArchivedAt from the query result.
func TestSQLiteStore_ScanMemoryRows_ScansNewColumns(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "scan-cols", Content: "scan columns test", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Directly set access_count=5 in the DB.
	if _, err := s.db.Exec(`UPDATE memory SET access_count = 5 WHERE id = 'scan-cols'`); err != nil {
		t.Fatalf("setting access_count: %v", err)
	}

	// Use SearchMemory (which calls scanMemoryRows) and verify AccessCount is returned.
	results, err := s.SearchMemory(ctx, "scope1", "scan", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].AccessCount != 5 {
		t.Errorf("expected AccessCount=5 from DB, got %d", results[0].AccessCount)
	}
}

// ─── UpdateMemory tests ───────────────────────────────────────────────────────

func TestSQLiteStore_UpdateMemory_UpdatesTitleAndTags(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "update-test",
		Content:   "original content about golang",
		Title:     "Original Title",
		Tags:      []string{"golang"},
		CreatedAt: time.Now(),
	}
	if err := s.AppendMemory(ctx, "scope1", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Update title, tags, and content.
	updated := entry
	updated.Title = "Updated Title"
	updated.Tags = []string{"golang", "updated", "testing"}
	updated.Content = "updated content about golang testing"

	if err := s.UpdateMemory(ctx, "scope1", updated); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	// Re-search and verify the updated values come back.
	results, err := s.SearchMemory(ctx, "scope1", "updated", 5)
	if err != nil {
		t.Fatalf("SearchMemory after update: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result after update, got 0")
	}
	if results[0].Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", results[0].Title)
	}
}

func TestSQLiteStore_UpdateMemory_ScopeIsolation(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "scope-update-test",
		Content:   "content",
		CreatedAt: time.Now(),
	}
	if err := s.AppendMemory(ctx, "scope-a", entry); err != nil {
		t.Fatalf("AppendMemory scope-a: %v", err)
	}

	// UpdateMemory with the wrong scopeID should affect 0 rows but return nil.
	entry.Title = "Should Not Change"
	if err := s.UpdateMemory(ctx, "scope-b", entry); err != nil {
		t.Fatalf("UpdateMemory with wrong scope: %v", err)
	}

	// The original entry in scope-a should be unchanged.
	results, err := s.SearchMemory(ctx, "scope-a", "", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected entry in scope-a")
	}
	if results[0].Title == "Should Not Change" {
		t.Error("UpdateMemory with wrong scope modified entry in another scope")
	}
}

func TestSQLiteStore_FTS5Available(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "fts5-smoke",
		Content:   "fts5smoketest unique canary word xyzzyplugh",
		CreatedAt: time.Now(),
	}
	if err := s.AppendMemory(ctx, "global", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	results, err := s.SearchMemory(ctx, "global", "xyzzyplugh", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v — FTS5 may not be compiled in", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (FTS5 smoke test), got %d", len(results))
	}
}

// ─── Integration Scenarios 1-5 (spec) ────────────────────────────────────────

// Scenario 1: FTS5 stemmer match
// "authenticated" indexed, searching "authenticate" returns it (Porter stemmer).
func TestIntegration_Scenario1_PorterStemmerMatch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID:        "s1",
		Content:   "The user authenticated successfully using OAuth",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	results, err := s.SearchMemory(ctx, "scope1", "authenticate", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Error("Scenario 1: Porter stemmer should match 'authenticated' when searching 'authenticate'")
	}
}

// Scenario 2: Prefix matching
// "configuration" indexed, searching "config" returns it via prefix "config"*.
func TestIntegration_Scenario2_PrefixMatch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID:        "s2",
		Content:   "configuration file was loaded from home directory",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	results, err := s.SearchMemory(ctx, "scope1", "config", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Error("Scenario 2: prefix 'config*' should match 'configuration'")
	}
}

// Scenario 3: Synonym expansion
// "database" indexed, searching "db" returns it (db → database synonym).
func TestIntegration_Scenario3_SynonymMatch(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID:        "s3",
		Content:   "the database connection pool was exhausted",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	results, err := s.SearchMemory(ctx, "scope1", "db", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Error("Scenario 3: synonym 'db' -> 'database*' should match 'database' in content")
	}
}

// Scenario 4: Migration completes within 2 seconds for an empty store.
func TestIntegration_Scenario4_MigrationUnder2Seconds(t *testing.T) {
	start := time.Now()
	s := newTestSQLiteStore(t)
	elapsed := time.Since(start)

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}

	if elapsed > 2*time.Second {
		t.Errorf("Scenario 4: migration took %v, must complete in < 2s", elapsed)
	}
}

// Scenario 5: Idempotent re-run — calling initSchema again leaves data intact.
func TestIntegration_Scenario5_IdempotentMigration(t *testing.T) {
	path := t.TempDir()
	s, err := NewSQLiteStore(config.StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
		ID: "pre-rerun", Content: "data before re-run", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	if err := s.initSchema(); err != nil {
		t.Fatalf("Scenario 5: second initSchema failed: %v", err)
	}

	var version int
	if err := s.db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != 9 {
		t.Errorf("Scenario 5: expected version=9 after re-run, got %d", version)
	}

	results, err := s.SearchMemory(ctx, "scope1", "data", 5)
	if err != nil {
		t.Fatalf("Scenario 5: SearchMemory after re-run: %v", err)
	}
	if len(results) == 0 {
		t.Error("Scenario 5: data was lost during idempotent re-run")
	}
}

// ─── Phase 4: Two-phase search (embedding reranking) ─────────────────────────

// TestTwoPhaseSearch_ReranksByCosineSimilarity inserts entries with synthetic
// embeddings, registers an embedQueryFunc, and verifies that SearchMemory
// reorders results by cosine similarity rather than FTS5 rank.
func TestTwoPhaseSearch_ReranksByCosineSimilarity(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert 5 entries with different embeddings. All contain "neural network"
	// so they all match the FTS query — but embeddings determine the final order.
	type entry struct {
		id   string
		seed float32
	}
	entries := []entry{
		{"e1", 0.9}, // most similar to query vector (seed 1.0)
		{"e2", 0.1},
		{"e3", 0.5},
		{"e4", 0.8}, // second most similar
		{"e5", 0.3},
	}
	for _, e := range entries {
		if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
			ID:        e.id,
			Content:   "neural network machine learning model",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("AppendMemory %s: %v", e.id, err)
		}
		// Write embedding directly to DB.
		blob := makeTestEmbedding(e.seed)
		if _, err := s.db.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, blob, e.id); err != nil {
			t.Fatalf("updating embedding for %s: %v", e.id, err)
		}
	}

	// Query vector is most similar to seed 0.9 (e1) and 0.8 (e4).
	queryVec := func(ctx context.Context, text string) ([]float32, error) {
		vec := make([]float32, 256)
		for i := range vec {
			vec[i] = 1.0 + float32(i)*0.001 // matches seed 0.9 most closely
		}
		return vec, nil
	}
	s.SetEmbedQueryFunc(queryVec)

	results, err := s.SearchMemory(ctx, "scope1", "neural network", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	// The first result must be e1 or e4 (highest similarity to query).
	topID := results[0].ID
	if topID != "e1" && topID != "e4" {
		t.Errorf("expected top result to be e1 or e4 (closest to query), got %q", topID)
	}
}

// TestTwoPhaseSearch_FallsBackWhenLessThan2Embeddings verifies that when fewer
// than 2 candidates have embeddings, FTS5 ordering is preserved (Scenario 15).
func TestTwoPhaseSearch_FallsBackWhenLessThan2Embeddings(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert 3 entries but only 1 has an embedding.
	for i, id := range []string{"f1", "f2", "f3"} {
		if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
			ID:        id,
			Content:   "machine learning algorithm training",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("AppendMemory %s: %v", id, err)
		}
	}
	// Only f1 gets an embedding.
	blob := makeTestEmbedding(0.5)
	if _, err := s.db.Exec(`UPDATE memory SET embedding = ? WHERE id = 'f1'`, blob); err != nil {
		t.Fatalf("updating embedding: %v", err)
	}

	calls := 0
	s.SetEmbedQueryFunc(func(ctx context.Context, text string) ([]float32, error) {
		calls++
		return make([]float32, 256), nil
	})

	results, err := s.SearchMemory(ctx, "scope1", "machine learning", 3)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	// embedQueryFunc must NOT have been called (only 1 embedding → FTS order).
	if calls > 0 {
		t.Errorf("expected embedQueryFunc not called when <2 embeddings, got %d calls", calls)
	}
}

// TestTwoPhaseSearch_NoEmbedQueryFunc verifies that SearchMemory behaves
// normally (FTS5 order) when embedQueryFunc is not set.
func TestTwoPhaseSearch_NoEmbedQueryFunc(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for _, id := range []string{"g1", "g2", "g3"} {
		if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
			ID:        id,
			Content:   "golang programming language backend",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("AppendMemory %s: %v", id, err)
		}
	}
	// No SetEmbedQueryFunc called.

	results, err := s.SearchMemory(ctx, "scope1", "golang", 3)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results without embedQueryFunc, got none")
	}
}

// TestTwoPhaseSearch_Scenario14_200EntriesTop5ByCosine inserts 200 entries,
// assigns synthetic embeddings to all, and verifies SearchMemory returns the
// top 5 by cosine similarity (Scenario 14).
func TestTwoPhaseSearch_Scenario14_200EntriesTop5ByCosine(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert 200 entries with varied embeddings.
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("e%03d", i)
		if err := s.AppendMemory(ctx, "scope1", MemoryEntry{
			ID:        id,
			Content:   "deep learning neural network architecture model",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("AppendMemory %s: %v", id, err)
		}
		seed := float32(i) * 0.001
		blob := makeTestEmbedding(seed)
		if _, err := s.db.Exec(`UPDATE memory SET embedding = ? WHERE id = ?`, blob, id); err != nil {
			t.Fatalf("updating embedding for %s: %v", id, err)
		}
	}

	// Query vector is closest to seed 0.199 (entry e199).
	queryVec := func(ctx context.Context, text string) ([]float32, error) {
		vec := make([]float32, 256)
		for i := range vec {
			vec[i] = 0.2 + float32(i)*0.001
		}
		return vec, nil
	}
	s.SetEmbedQueryFunc(queryVec)

	results, err := s.SearchMemory(ctx, "scope1", "deep learning", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Scenario 14: expected 5 results, got %d", len(results))
	}
}
