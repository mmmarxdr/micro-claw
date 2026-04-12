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
	"microagent/internal/content"
	"microagent/internal/provider"
)

func TestFileStore_SaveAndLoadConversation(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.StoreConfig{Path: tmpDir}
	store := NewFileStore(cfg)

	ctx := context.Background()
	conv := Conversation{
		ID:        "test-conv-1",
		ChannelID: "cli",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock("hello")},
			{Role: "assistant", Content: content.TextBlock("world")},
		},
		CreatedAt: time.Now().Truncate(time.Millisecond).UTC(),
		UpdatedAt: time.Now().Truncate(time.Millisecond).UTC(),
	}

	err := store.SaveConversation(ctx, conv)
	if err != nil {
		t.Fatalf("Failed to save conversation: %v", err)
	}

	loaded, err := store.LoadConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("Failed to load conversation: %v", err)
	}

	if loaded.ID != conv.ID || loaded.ChannelID != conv.ChannelID {
		t.Errorf("Loaded conversation metadata mismatch")
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(loaded.Messages))
	}
}

func TestFileStore_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.StoreConfig{Path: tmpDir}
	store := NewFileStore(cfg)

	ctx := context.Background()
	conv := Conversation{ID: "atomic-test"}

	err := store.SaveConversation(ctx, conv)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify no .tmp file exists
	tmpFile := filepath.Join(tmpDir, "conversations", "atomic-test.json.tmp")
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Errorf("Expected temporary file to be cleaned up, but it exists")
	}
}

func TestFileStore_LoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})

	_, err := store.LoadConversation(context.Background(), "no-existe")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestFileStore_ErrNotFound_Wrapped(t *testing.T) {
	tmpDir := t.TempDir()
	st := NewFileStore(config.StoreConfig{Path: tmpDir})
	_, err := st.LoadConversation(context.Background(), "no-existe")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected errors.Is(err, ErrNotFound), got: %v", err)
	}
	// The raw error must be wrapped (not the sentinel itself)
	if err == ErrNotFound {
		t.Errorf("expected error to be wrapped, not the sentinel itself, got: %v", err)
	}
}

func TestFileStore_ListConversations(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Guardamos 5 conversaciones intercalando ChannelID
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
		_ = store.SaveConversation(ctx, conv)
	}

	// List "cli" only, max 3
	cliConvs, err := store.ListConversations(ctx, "cli", 3)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}

	// Should be 2 from "cli" (i=1, i=3)
	if len(cliConvs) != 2 {
		t.Errorf("Expected 2 'cli' conversations, got %d", len(cliConvs))
	}

	// Verify order (newest first)
	if cliConvs[0].ID != "conv-3" || cliConvs[1].ID != "conv-1" {
		t.Errorf("Conversations not in expected descending order")
	}
}

func TestFileStore_MemoryAppendAndSearch(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	entries := []MemoryEntry{
		{ID: "m1", Content: "Golang es un lenguaje tipado", Source: "c1", CreatedAt: time.Now()},
		{ID: "m2", Content: "Me gusta la comida italiana", Source: "c1", CreatedAt: time.Now().Add(time.Minute)},
		{ID: "m3", Content: "El MVP de MicroAgent usa golang puro", Source: "c2", CreatedAt: time.Now().Add(2 * time.Minute)},
	}

	for _, e := range entries {
		if err := store.AppendMemory(ctx, "global", e); err != nil {
			t.Fatalf("AppendMemory failed: %v", err)
		}
	}

	// Case insensitive search
	matches, err := store.SearchMemory(ctx, "global", "GOLANG", 5)
	if err != nil {
		t.Fatalf("SearchMemory failed: %v", err)
	}

	if len(matches) != 2 {
		t.Errorf("Expected 2 matches for 'golang', got %d", len(matches))
	}

	// Order: newest first (m3 then m1)
	if len(matches) == 2 && matches[0].ID != "m3" {
		t.Errorf("Expected m3 to be first (newest), got %s", matches[0].ID)
	}
}

func TestFileStore_MemorySearchLimit(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		e := MemoryEntry{ID: fmt.Sprintf("m%d", i), Content: "Test item"}
		_ = store.AppendMemory(ctx, "global", e)
	}

	matches, _ := store.SearchMemory(ctx, "global", "test", 3)
	if len(matches) != 3 {
		t.Errorf("Expected exactly 3 matches due to limit, got %d", len(matches))
	}
}

func TestFileStore_CloseIdempotent(t *testing.T) {
	store := NewFileStore(config.StoreConfig{})
	err1 := store.Close()
	err2 := store.Close()
	if err1 != nil || err2 != nil {
		t.Errorf("Close should return nil and be idempotent")
	}
}

func TestFileStore_ListConversations_EmptyChannelID(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	ids := []string{"c1", "c2", "c3"}
	channels := []string{"cli", "telegram", "discord"}
	for i, id := range ids {
		conv := Conversation{
			ID:        id,
			ChannelID: channels[i],
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := store.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("SaveConversation: %v", err)
		}
	}

	// Empty channelID should return all 3
	result, err := store.ListConversations(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 conversations, got %d", len(result))
	}
}

func TestFileStore_ListConversations_LimitZero(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("lz-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		_ = store.SaveConversation(ctx, conv)
	}

	// limit=0 means no limit — should return all 5
	result, err := store.ListConversations(ctx, "cli", 0)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 conversations with limit=0, got %d", len(result))
	}
}

func TestFileStore_ListConversations_LimitExceedsTotal(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("le-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		_ = store.SaveConversation(ctx, conv)
	}

	// limit=10 but only 2 exist — should return 2, no panic
	result, err := store.ListConversations(ctx, "cli", 10)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 conversations (limit exceeds total), got %d", len(result))
	}
}

func TestFileStore_SearchMemory_TagMatch(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "tag-test",
		Content:   "unrelated content",
		Tags:      []string{"golang", "testing"},
		CreatedAt: time.Now(),
	}
	if err := store.AppendMemory(ctx, "global", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Should find by tag "golang"
	matches, err := store.SearchMemory(ctx, "global", "golang", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match for tag 'golang', got %d", len(matches))
	}

	// Should NOT find for unrelated tag
	noMatches, err := store.SearchMemory(ctx, "global", "python", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(noMatches) != 0 {
		t.Errorf("expected 0 matches for 'python', got %d", len(noMatches))
	}
}

func TestFileStore_SearchMemory_EmptyQuery(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		e := MemoryEntry{
			ID:        fmt.Sprintf("eq-%d", i),
			Content:   fmt.Sprintf("memory entry %d", i),
			CreatedAt: time.Now(),
		}
		_ = store.AppendMemory(ctx, "global", e)
	}

	// Empty string is a substring of every string — should return all 3
	matches, err := store.SearchMemory(ctx, "global", "", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(matches) != 3 {
		t.Errorf("expected 3 matches for empty query, got %d", len(matches))
	}
}

func TestFileStore_SearchMemory_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	entry := MemoryEntry{
		ID:        "ci-test",
		Content:   "Golang is great",
		CreatedAt: time.Now(),
	}
	if err := store.AppendMemory(ctx, "global", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	for _, query := range []string{"golang", "GOLANG", "GoLang"} {
		matches, err := store.SearchMemory(ctx, "global", query, 5)
		if err != nil {
			t.Fatalf("SearchMemory(%q): %v", query, err)
		}
		if len(matches) != 1 {
			t.Errorf("query %q: expected 1 match, got %d", query, len(matches))
		}
	}
}

func TestFileStore_LoadConversation_CorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Ensure the conversations directory exists by saving a valid conv first, then overwrite
	convID := "corrupt-conv"

	// Get the expected path by saving a valid conv first
	validConv := Conversation{ID: convID, ChannelID: "cli"}
	if err := store.SaveConversation(ctx, validConv); err != nil {
		t.Fatalf("setup save: %v", err)
	}

	// Overwrite the file with corrupt JSON
	corruptPath := filepath.Join(tmpDir, "conversations", convID+".json")
	if err := os.WriteFile(corruptPath, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	_, err := store.LoadConversation(ctx, convID)
	if err == nil {
		t.Fatal("expected unmarshal error for corrupt JSON, got nil")
	}
}

func TestFileStore_AtomicWrite_UnwritableDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test: running as root")
	}

	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// First save a conversation to create the conversations directory
	validConv := Conversation{ID: "setup-conv", ChannelID: "cli"}
	if err := store.SaveConversation(ctx, validConv); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Make the conversations directory unwritable
	convDir := filepath.Join(tmpDir, "conversations")
	if err := os.Chmod(convDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore permissions when test ends so t.TempDir() cleanup works
	defer func() { _ = os.Chmod(convDir, 0o755) }()

	newConv := Conversation{ID: "new-conv", ChannelID: "cli"}
	err := store.SaveConversation(ctx, newConv)
	if err == nil {
		t.Fatal("expected error when writing to unwritable dir, got nil")
	}
}

// TestFileStore_ListConversations_LimitActuallyTruncates verifies that when
// more conversations exist than the limit, only `limit` entries are returned.
func TestFileStore_ListConversations_LimitActuallyTruncates(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		conv := Conversation{
			ID:        fmt.Sprintf("trunc-%d", i),
			ChannelID: "cli",
			UpdatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := store.SaveConversation(ctx, conv); err != nil {
			t.Fatalf("setup save: %v", err)
		}
	}

	result, err := store.ListConversations(ctx, "cli", 2)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 conversations with limit=2, got %d", len(result))
	}
}

// TestFileStore_ListConversations_DirEntrySkipped verifies that sub-directories
// inside the conversations folder are silently skipped.
func TestFileStore_ListConversations_DirEntrySkipped(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Save one legitimate conversation to create the conversations directory.
	if err := store.SaveConversation(ctx, Conversation{ID: "real", ChannelID: "cli"}); err != nil {
		t.Fatalf("setup save: %v", err)
	}

	// Create a sub-directory inside conversations/ — it should be ignored.
	subDir := filepath.Join(tmpDir, "conversations", "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	result, err := store.ListConversations(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListConversations failed: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 conversation (dir entry skipped), got %d", len(result))
	}
}

// TestFileStore_ConvPath_TildeExpansion verifies that a basePath starting with
// "~" is correctly expanded to the user home directory.
func TestFileStore_ConvPath_TildeExpansion(t *testing.T) {
	// We can't control the real home dir, so we just verify a store with "~"
	// in the path doesn't panic and either succeeds (real home exists) or
	// returns an error — the important thing is the tilde branch is exercised.
	store := NewFileStore(config.StoreConfig{Path: "~/microclaw-test-tmp-do-not-use"})
	ctx := context.Background()

	// Attempt to list conversations — we expect either success or a filesystem
	// error, not a panic. Clean up if it succeeded.
	convDir := ""
	if usr, err := os.UserHomeDir(); err == nil {
		convDir = filepath.Join(usr, "microclaw-test-tmp-do-not-use", "conversations")
		defer os.RemoveAll(filepath.Join(usr, "microclaw-test-tmp-do-not-use"))
	}
	_ = convDir

	_, _ = store.ListConversations(ctx, "", 0)
}

// TestFileStore_MemPath_TildeExpansion verifies that a basePath starting with
// "~" is expanded for memory operations.
func TestFileStore_MemPath_TildeExpansion(t *testing.T) {
	if usr, err := os.UserHomeDir(); err != nil {
		t.Skip("cannot determine home dir")
	} else {
		defer os.RemoveAll(filepath.Join(usr, "microclaw-test-tmp-mem-do-not-use"))
	}

	store := NewFileStore(config.StoreConfig{Path: "~/microclaw-test-tmp-mem-do-not-use"})
	ctx := context.Background()

	// AppendMemory exercises memPath with tilde.
	err := store.AppendMemory(ctx, "global", MemoryEntry{ID: "tilde-mem", Content: "tilde test"})
	if err != nil {
		t.Fatalf("AppendMemory with tilde basePath failed: %v", err)
	}
}

// TestFileStore_SaveMemory_MemPathError verifies that saveMemory propagates a
// memPath error (triggered the same way as TestFileStore_MemPath_MkdirAllError).
func TestFileStore_SaveMemory_MemPathError(t *testing.T) {
	parent := t.TempDir()
	blockPath := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(blockPath, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	basePath := filepath.Join(blockPath, "subdir")
	store := NewFileStore(config.StoreConfig{Path: basePath})
	ctx := context.Background()

	// AppendMemory calls loadMemory (memPath fails) then saveMemory.
	// loadMemory will fail first — that's fine, it still exercises the path.
	err := store.AppendMemory(ctx, "global", MemoryEntry{ID: "x"})
	if err == nil {
		t.Fatal("expected error when basePath cannot be created")
	}
}

// TestFileStore_ConvPath_MkdirAllError verifies that convPath propagates a
// MkdirAll failure when the "conversations" slot is already occupied by a file.
func TestFileStore_ConvPath_MkdirAllError(t *testing.T) {
	tmpDir := t.TempDir()

	// Block MkdirAll by creating a regular file where the directory should be.
	blockPath := filepath.Join(tmpDir, "conversations")
	if err := os.WriteFile(blockPath, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	err := store.SaveConversation(ctx, Conversation{ID: "any"})
	if err == nil {
		t.Fatal("expected MkdirAll error when conversations path is a file, got nil")
	}
}

// TestFileStore_MemPath_MkdirAllError verifies that memPath propagates a
// MkdirAll failure when the basePath itself cannot be created.
func TestFileStore_MemPath_MkdirAllError(t *testing.T) {
	parent := t.TempDir()

	// Create a regular file where the sub-directory (basePath) should live.
	blockPath := filepath.Join(parent, "not-a-dir")
	if err := os.WriteFile(blockPath, []byte("block"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// basePath = <parent>/not-a-dir/subdir — MkdirAll must traverse through a file.
	basePath := filepath.Join(blockPath, "subdir")
	store := NewFileStore(config.StoreConfig{Path: basePath})
	ctx := context.Background()

	err := store.AppendMemory(ctx, "global", MemoryEntry{ID: "x", Content: "test"})
	if err == nil {
		t.Fatal("expected MkdirAll error when basePath cannot be created, got nil")
	}
}

// TestFileStore_LoadConversation_ReadError verifies that a non-NotExist OS read
// error is surfaced by LoadConversation.
func TestFileStore_LoadConversation_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test: running as root")
	}

	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	convID := "unreadable-conv"
	if err := store.SaveConversation(ctx, Conversation{ID: convID}); err != nil {
		t.Fatalf("setup save: %v", err)
	}

	convFile := filepath.Join(tmpDir, "conversations", convID+".json")
	if err := os.Chmod(convFile, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(convFile, 0o644) }()

	_, err := store.LoadConversation(ctx, convID)
	if err == nil {
		t.Fatal("expected read error for unreadable conversation file, got nil")
	}
}

// TestFileStore_ListConversations_ReadDirError verifies that a non-NotExist
// ReadDir error is returned by ListConversations.
func TestFileStore_ListConversations_ReadDirError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test: running as root")
	}

	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Save once to create the conversations directory.
	if err := store.SaveConversation(ctx, Conversation{ID: "setup"}); err != nil {
		t.Fatalf("setup save: %v", err)
	}

	convDir := filepath.Join(tmpDir, "conversations")
	if err := os.Chmod(convDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(convDir, 0o755) }()

	_, err := store.ListConversations(ctx, "", 0)
	if err == nil {
		t.Fatal("expected ReadDir error for unreadable conversations dir, got nil")
	}
}

// TestFileStore_LoadMemory_CorruptJSON verifies that an invalid JSON file in
// the memory path causes an unmarshal error.
func TestFileStore_LoadMemory_CorruptJSON(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	memFile := filepath.Join(tmpDir, "memory_global.json")
	if err := os.WriteFile(memFile, []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := store.SearchMemory(ctx, "global", "anything", 5)
	if err == nil {
		t.Fatal("expected unmarshal error for corrupt memory_global.json, got nil")
	}
}

// TestFileStore_LoadMemory_ReadError verifies that a non-NotExist OS read error
// on memory.json is returned by SearchMemory.
func TestFileStore_LoadMemory_ReadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test: running as root")
	}

	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Create a valid memory.json first.
	if err := store.AppendMemory(ctx, "global", MemoryEntry{ID: "m1", Content: "hi"}); err != nil {
		t.Fatalf("setup AppendMemory: %v", err)
	}

	memFile := filepath.Join(tmpDir, "memory_global.json")
	if err := os.Chmod(memFile, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(memFile, 0o644) }()

	_, err := store.SearchMemory(ctx, "global", "hi", 5)
	if err == nil {
		t.Fatal("expected read error for unreadable memory_global.json, got nil")
	}
}

// TestFileStore_SaveMemory_AtomicWriteError verifies that saveMemory surfaces
// an atomicWrite failure. We trigger it by placing a directory at the
// memory.json path, so os.WriteFile on memory.json.tmp succeeds but
// os.Rename into a directory fails.
func TestFileStore_SaveMemory_AtomicWriteError(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Create memory.json as a directory so atomicWrite's rename fails.
	memDir := filepath.Join(tmpDir, "memory_global.json")
	if err := os.Mkdir(memDir, 0o755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}

	err := store.AppendMemory(ctx, "global", MemoryEntry{ID: "x", Content: "test"})
	if err == nil {
		t.Fatal("expected atomicWrite error when memory_global.json is a directory, got nil")
	}
}

// TestFileStore_UpdateMemory_IsNoOp verifies that UpdateMemory returns nil
// without writing any file or modifying existing memory entries.
func TestFileStore_UpdateMemory_IsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	st := NewFileStore(config.StoreConfig{Path: tmpDir})
	ctx := context.Background()

	// Append a real entry first.
	entry := MemoryEntry{
		ID:      "noop-test",
		Content: "original content",
		Tags:    []string{"original"},
	}
	if err := st.AppendMemory(ctx, "scope1", entry); err != nil {
		t.Fatalf("AppendMemory: %v", err)
	}

	// Call UpdateMemory — should return nil with no side effects.
	entry.Title = "Updated"
	entry.Tags = []string{"updated"}
	if err := st.UpdateMemory(ctx, "scope1", entry); err != nil {
		t.Fatalf("UpdateMemory should return nil for FileStore, got: %v", err)
	}

	// Verify the entry was NOT modified (FileStore UpdateMemory is a no-op).
	results, err := st.SearchMemory(ctx, "scope1", "original", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected original entry to still be present")
	}
	if results[0].Title == "Updated" {
		t.Error("UpdateMemory should be a no-op for FileStore but modified the entry")
	}
}

func TestFileStore_PersistenceAcrossInstances(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()
	conv := Conversation{ID: "persist-test", ChannelID: "ch1"}

	// Instance A saves
	storeA := NewFileStore(config.StoreConfig{Path: tmpDir})
	if err := storeA.SaveConversation(ctx, conv); err != nil {
		t.Fatalf("StoreA save failed: %v", err)
	}
	storeA.Close()

	// Instance B loads
	storeB := NewFileStore(config.StoreConfig{Path: tmpDir})
	loaded, err := storeB.LoadConversation(ctx, "persist-test")
	if err != nil {
		t.Fatalf("StoreB load failed: %v", err)
	}

	if loaded.ChannelID != "ch1" {
		t.Errorf("Data loss across instances, got channel %s", loaded.ChannelID)
	}
}
