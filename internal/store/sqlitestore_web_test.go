package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"daimon/internal/content"
	"daimon/internal/provider"
)

// makeConv is a helper to build and save a minimal Conversation for web tests.
func makeConv(t *testing.T, s *SQLiteStore, id, channelID string) Conversation {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	conv := Conversation{
		ID:        id,
		ChannelID: channelID,
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock("hi")},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("SaveConversation(%s): %v", id, err)
	}
	return conv
}

// ─── T1.6: ListConversationsPaginated ───────────────────────────────────────

func TestListConversationsPaginated_Empty(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	convs, total, err := s.ListConversationsPaginated(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("ListConversationsPaginated: %v", err)
	}
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}
	if len(convs) != 0 {
		t.Errorf("expected 0 conversations, got %d", len(convs))
	}
}

func TestListConversationsPaginated_OnePage(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	makeConv(t, s, "c1", "telegram:123")
	makeConv(t, s, "c2", "telegram:456")
	makeConv(t, s, "c3", "discord:789")

	convs, total, err := s.ListConversationsPaginated(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("ListConversationsPaginated: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}
	if len(convs) != 3 {
		t.Errorf("expected 3 conversations, got %d", len(convs))
	}
}

func TestListConversationsPaginated_OffsetBeyondTotal(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	makeConv(t, s, "c1", "cli")
	makeConv(t, s, "c2", "cli")

	convs, total, err := s.ListConversationsPaginated(ctx, "", 10, 100)
	if err != nil {
		t.Fatalf("ListConversationsPaginated: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if len(convs) != 0 {
		t.Errorf("expected 0 conversations for offset beyond total, got %d", len(convs))
	}
}

func TestListConversationsPaginated_ChannelFilter(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	makeConv(t, s, "tg1", "telegram:111")
	makeConv(t, s, "tg2", "telegram:222")
	makeConv(t, s, "dc1", "discord:333")

	// Filter by telegram prefix.
	convs, total, err := s.ListConversationsPaginated(ctx, "telegram:", 10, 0)
	if err != nil {
		t.Fatalf("ListConversationsPaginated(telegram:): %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2 for telegram filter, got %d", total)
	}
	if len(convs) != 2 {
		t.Errorf("expected 2 telegram conversations, got %d", len(convs))
	}
	for _, c := range convs {
		if c.ChannelID[:len("telegram:")] != "telegram:" {
			t.Errorf("unexpected channelID %q in telegram-filtered results", c.ChannelID)
		}
	}
}

func TestListConversationsPaginated_Pagination(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert 5 conversations.
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		makeConv(t, s, "conv-"+id, "cli")
	}

	// Page 1: first 3.
	page1, total, err := s.ListConversationsPaginated(ctx, "", 3, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if len(page1) != 3 {
		t.Errorf("expected 3 on page1, got %d", len(page1))
	}

	// Page 2: remaining 2.
	page2, total2, err := s.ListConversationsPaginated(ctx, "", 3, 3)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if total2 != 5 {
		t.Errorf("expected total=5 on page2, got %d", total2)
	}
	if len(page2) != 2 {
		t.Errorf("expected 2 on page2, got %d", len(page2))
	}
}

// ─── T1.7: DeleteConversation ────────────────────────────────────────────────

func TestDeleteConversation_Success(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	makeConv(t, s, "del-me", "cli")

	if err := s.DeleteConversation(ctx, "del-me"); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}

	// Loading it should return ErrNotFound.
	_, err := s.LoadConversation(ctx, "del-me")
	if err == nil {
		t.Fatal("expected error after deletion, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteConversation_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.DeleteConversation(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─── T1.8: DeleteMemory ──────────────────────────────────────────────────────

func TestDeleteMemory_Success(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	scopeID := "test-scope"
	entry := MemoryEntry{
		ID:        "mem-1",
		ScopeID:   scopeID,
		Content:   "remember this",
		Source:    "conv-1",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.AppendMemory(ctx, scopeID, entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Fetch the rowid for the inserted entry.
	var rowid int64
	err := s.db.QueryRowContext(ctx,
		`SELECT rowid FROM memory WHERE id = ?`, entry.ID,
	).Scan(&rowid)
	if err != nil {
		t.Fatalf("fetching rowid: %v", err)
	}

	if err := s.DeleteMemory(ctx, scopeID, rowid); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	// Entry should be gone from the main table.
	var count int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory WHERE id = ?`, entry.ID).Scan(&count)
	if count != 0 {
		t.Errorf("expected memory entry to be deleted, COUNT=%d", count)
	}
}

func TestDeleteMemory_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	err := s.DeleteMemory(ctx, "no-scope", 9999)
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteMemory_FTS5Cleanup(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	scopeID := "fts-scope"
	entry := MemoryEntry{
		ID:        "mem-fts",
		ScopeID:   scopeID,
		Content:   "unique searchable phrase for deletion test",
		Source:    "conv-fts",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.AppendMemory(ctx, scopeID, entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Confirm it's searchable before deletion.
	results, err := s.SearchMemory(ctx, scopeID, "unique searchable phrase", 10)
	if err != nil {
		t.Fatalf("SearchMemory before delete: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected entry to be searchable before deletion")
	}

	// Fetch rowid and delete.
	var rowid int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT rowid FROM memory WHERE id = ?`, entry.ID,
	).Scan(&rowid); err != nil {
		t.Fatalf("fetching rowid: %v", err)
	}

	if err := s.DeleteMemory(ctx, scopeID, rowid); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	// Confirm FTS5 entry is removed — searching should return nothing.
	results, err = s.SearchMemory(ctx, scopeID, "unique searchable phrase", 10)
	if err != nil {
		t.Fatalf("SearchMemory after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after deletion (FTS5 cleanup), got %d", len(results))
	}
}

// ─── T1.3 (CountConversations standalone test) ───────────────────────────────

func TestCountConversations(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	n, err := s.CountConversations(ctx, "")
	if err != nil {
		t.Fatalf("CountConversations empty: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}

	makeConv(t, s, "c1", "telegram:1")
	makeConv(t, s, "c2", "telegram:2")
	makeConv(t, s, "c3", "discord:1")

	all, err := s.CountConversations(ctx, "")
	if err != nil {
		t.Fatalf("CountConversations all: %v", err)
	}
	if all != 3 {
		t.Errorf("expected 3, got %d", all)
	}

	tg, err := s.CountConversations(ctx, "telegram:")
	if err != nil {
		t.Fatalf("CountConversations telegram: %v", err)
	}
	if tg != 2 {
		t.Errorf("expected 2 telegram conversations, got %d", tg)
	}
}

// ─── interface compliance check ──────────────────────────────────────────────

var _ WebStore = (*SQLiteStore)(nil)
