package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Test store helper
// ---------------------------------------------------------------------------

// newTestMemoryStore creates a real SQLiteStore backed by a temp directory.
// It is closed automatically when the test ends.
func newTestMemoryStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedMemory saves a MemoryEntry directly and returns it (with the given ID).
func seedMemory(t *testing.T, st store.Store, scope string, entry store.MemoryEntry) store.MemoryEntry {
	t.Helper()
	if entry.ID == "" {
		entry.ID = "test-id-" + entry.Title
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.Importance == 0 {
		entry.Importance = 5
	}
	if err := st.AppendMemory(context.Background(), scope, entry); err != nil {
		t.Fatalf("seedMemory: %v", err)
	}
	return entry
}

// buildDeps constructs a MemoryToolDeps with the given store and nil callbacks.
func buildDeps(st store.Store) MemoryToolDeps {
	return MemoryToolDeps{Store: st}
}

// execTool is a convenience wrapper: marshal params to JSON and call Execute.
func execTool(t *testing.T, tool Tool, ctx context.Context, params any) ToolResult {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// T3.5: Unit tests
// ---------------------------------------------------------------------------

// 1. TestSaveMemory_Success
func TestSaveMemory_Success(t *testing.T) {
	st := newTestMemoryStore(t)
	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	scope := "chan1:user1"
	ctx := WithScope(context.Background(), scope)

	result := execTool(t, tools["save_memory"], ctx, map[string]any{
		"content": "The user prefers dark mode.",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.Content)
	}
	if !strings.HasPrefix(result.Content, "Memory saved:") {
		t.Errorf("unexpected response: %q", result.Content)
	}

	// Verify it was actually written to the store.
	entries, err := st.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Content != "The user prefers dark mode." {
		t.Errorf("content mismatch: %q", entries[0].Content)
	}
	if entries[0].Importance != 7 {
		t.Errorf("expected Importance=7, got %d", entries[0].Importance)
	}
	if entries[0].Source != "tool:save_memory" {
		t.Errorf("expected Source=tool:save_memory, got %q", entries[0].Source)
	}
}

// 2. TestSaveMemory_WithTopicAndType
func TestSaveMemory_WithTopicAndType(t *testing.T) {
	st := newTestMemoryStore(t)
	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	scope := "chan1:user1"
	ctx := WithScope(context.Background(), scope)

	result := execTool(t, tools["save_memory"], ctx, map[string]any{
		"content": "User dislikes spicy food.",
		"topic":   "food",
		"type":    "preference",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	entries, err := st.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one entry")
	}
	e := entries[0]
	if e.Topic != "food" {
		t.Errorf("expected topic=food, got %q", e.Topic)
	}
	if e.Type != "preference" {
		t.Errorf("expected type=preference, got %q", e.Type)
	}
}

// 3. TestSaveMemory_MissingContent_Error
func TestSaveMemory_MissingContent_Error(t *testing.T) {
	st := newTestMemoryStore(t)
	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), "chan1:user1")

	result := execTool(t, tools["save_memory"], ctx, map[string]any{
		"content": "",
	})

	if !result.IsError {
		t.Fatalf("expected IsError=true, got content: %q", result.Content)
	}
}

// 4. TestSearchMemory_Success
func TestSearchMemory_Success(t *testing.T) {
	st := newTestMemoryStore(t)
	scope := "chan1:user1"

	seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-1", Title: "Dark mode preference", Content: "User prefers dark mode.",
	})
	seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-2", Title: "Spicy food dislike", Content: "User dislikes spicy food.",
	})
	seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-3", Title: "Favorite language", Content: "User loves Go.",
	})

	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), scope)
	result := execTool(t, tools["search_memory"], ctx, map[string]any{
		"query": "preference",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content == "No memories found." {
		t.Fatal("expected results, got 'No memories found.'")
	}
	// Should contain numbered list format.
	if !strings.Contains(result.Content, "1.") {
		t.Errorf("expected numbered list, got: %q", result.Content)
	}
}

// 5. TestSearchMemory_NoResults
func TestSearchMemory_NoResults(t *testing.T) {
	st := newTestMemoryStore(t)
	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), "chan1:user1")
	result := execTool(t, tools["search_memory"], ctx, map[string]any{
		"query": "anything",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "No memories found." {
		t.Errorf("expected 'No memories found.', got: %q", result.Content)
	}
}

// 6. TestSearchMemory_WithTopicFilter
func TestSearchMemory_WithTopicFilter(t *testing.T) {
	st := newTestMemoryStore(t)
	scope := "chan1:user1"

	seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-1", Title: "Food pref", Content: "User dislikes spicy food.", Topic: "food",
	})

	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), scope)
	// Topic is appended to the query — this tests that the tool does so without panicking.
	result := execTool(t, tools["search_memory"], ctx, map[string]any{
		"query": "preference",
		"topic": "food",
	})

	// We don't assert exact results since FTS depends on index; just confirm no error.
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
}

// 7. TestUpdateMemory_Success
func TestUpdateMemory_Success(t *testing.T) {
	st := newTestMemoryStore(t)
	scope := "chan1:user1"

	e := seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-upd", Title: "Old title", Content: "Old content.",
	})

	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), scope)
	result := execTool(t, tools["update_memory"], ctx, map[string]any{
		"id":      e.ID,
		"content": "Updated content.",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.HasPrefix(result.Content, "Memory updated:") {
		t.Errorf("unexpected response: %q", result.Content)
	}

	// Verify content was updated in store.
	entries, err := st.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	var found bool
	for _, en := range entries {
		if en.ID == e.ID {
			found = true
			if en.Content != "Updated content." {
				t.Errorf("content not updated: %q", en.Content)
			}
			break
		}
	}
	if !found {
		t.Error("entry not found after update")
	}
}

// 8. TestForgetMemory_Success
func TestForgetMemory_Success(t *testing.T) {
	st := newTestMemoryStore(t)
	scope := "chan1:user1"

	e := seedMemory(t, st, scope, store.MemoryEntry{
		ID: "mem-forget", Title: "To forget", Content: "This should be archived.",
	})

	deps := buildDeps(st)
	tools := BuildMemoryTools(deps)

	ctx := WithScope(context.Background(), scope)
	result := execTool(t, tools["forget_memory"], ctx, map[string]any{
		"id": e.ID,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.HasPrefix(result.Content, "Memory forgotten:") {
		t.Errorf("unexpected response: %q", result.Content)
	}

	// Verify the entry is now archived (not returned by SearchMemory).
	entries, err := st.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	for _, en := range entries {
		if en.ID == e.ID {
			t.Error("entry still visible after forget (should be archived)")
		}
	}
}

// 9. TestScopeFromContext
func TestScopeFromContext(t *testing.T) {
	// No scope set — returns "".
	ctx := context.Background()
	if got := ScopeFromContext(ctx); got != "" {
		t.Errorf("expected empty scope, got %q", got)
	}

	// Scope set via WithScope.
	ctx2 := WithScope(ctx, "channel:sender")
	if got := ScopeFromContext(ctx2); got != "channel:sender" {
		t.Errorf("expected 'channel:sender', got %q", got)
	}

	// Nested WithScope — inner wins.
	ctx3 := WithScope(ctx2, "override")
	if got := ScopeFromContext(ctx3); got != "override" {
		t.Errorf("expected 'override', got %q", got)
	}

	// Original context is unaffected.
	if got := ScopeFromContext(ctx2); got != "channel:sender" {
		t.Errorf("ctx2 should still be 'channel:sender', got %q", got)
	}
}
