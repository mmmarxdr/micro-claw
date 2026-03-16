package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/provider"
)

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
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
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
	if loaded.Messages[0].Role != "user" || loaded.Messages[0].Content != "hello" {
		t.Errorf("message[0] mismatch: %+v", loaded.Messages[0])
	}
	if loaded.Messages[1].Role != "assistant" || loaded.Messages[1].Content != "world" {
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
