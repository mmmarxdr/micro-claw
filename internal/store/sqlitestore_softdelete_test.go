package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"daimon/internal/content"
	"daimon/internal/provider"
)

// seedConv writes a conv with N text messages all from the "user" role for
// tests that want to exercise paginated reads.
func seedConv(t *testing.T, s *SQLiteStore, id, channelID string, messageCount int) {
	t.Helper()
	msgs := make([]provider.ChatMessage, 0, messageCount)
	for i := 0; i < messageCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, provider.ChatMessage{
			Role:    role,
			Content: content.Blocks{{Type: content.BlockText, Text: "msg-" + itoa(i)}},
		})
	}
	conv := Conversation{
		ID:        id,
		ChannelID: channelID,
		Messages:  msgs,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := s.SaveConversation(context.Background(), conv); err != nil {
		t.Fatalf("seed SaveConversation: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- B1. Soft delete + restore ---

func TestDeleteConversation_SetsDeletedAt(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_x", "web:t1", 3)

	if err := s.DeleteConversation(context.Background(), "conv_x"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	var deletedAt *time.Time
	if err := s.db.QueryRow(`SELECT deleted_at FROM conversations WHERE id = ?`, "conv_x").Scan(&deletedAt); err != nil {
		t.Fatalf("reading deleted_at: %v", err)
	}
	if deletedAt == nil || deletedAt.IsZero() {
		t.Errorf("deleted_at should be set, got %v", deletedAt)
	}
}

func TestDeleteConversation_AlreadyDeletedIsNoOp(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_y", "web:t1", 1)

	if err := s.DeleteConversation(context.Background(), "conv_y"); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	var first *time.Time
	if err := s.db.QueryRow(`SELECT deleted_at FROM conversations WHERE id = ?`, "conv_y").Scan(&first); err != nil {
		t.Fatalf("reading deleted_at: %v", err)
	}

	// Second call: no-op, no error.
	if err := s.DeleteConversation(context.Background(), "conv_y"); err != nil {
		t.Fatalf("second delete should be no-op, got: %v", err)
	}
	var second *time.Time
	if err := s.db.QueryRow(`SELECT deleted_at FROM conversations WHERE id = ?`, "conv_y").Scan(&second); err != nil {
		t.Fatalf("reading deleted_at after second: %v", err)
	}
	if first == nil || second == nil || !first.Equal(*second) {
		t.Errorf("deleted_at should remain at the earliest value (first=%v, second=%v)", first, second)
	}
}

func TestDeleteConversation_NonexistentReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	err := s.DeleteConversation(context.Background(), "conv_nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRestoreConversation_ClearsDeletedAt(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_r", "web:t1", 2)

	_ = s.DeleteConversation(context.Background(), "conv_r")
	if err := s.RestoreConversation(context.Background(), "conv_r"); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// After restore, LoadConversation should return it.
	got, err := s.LoadConversation(context.Background(), "conv_r")
	if err != nil {
		t.Fatalf("Load after restore: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil conv after restore")
	}
}

func TestRestoreConversation_LiveReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_live", "web:t1", 1)

	err := s.RestoreConversation(context.Background(), "conv_live")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for live conv, got %v", err)
	}
}

func TestRestoreConversation_NonexistentReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	err := s.RestoreConversation(context.Background(), "conv_ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// --- B2. Read-path filter ---

func TestLoadConversation_SoftDeletedReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_sd", "web:t1", 1)
	_ = s.DeleteConversation(context.Background(), "conv_sd")

	_, err := s.LoadConversation(context.Background(), "conv_sd")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for soft-deleted conv, got %v", err)
	}
}

func TestListConversationsPaginated_ExcludesSoftDeleted(t *testing.T) {
	s := newTestSQLiteStore(t)
	for i := 0; i < 5; i++ {
		seedConv(t, s, "conv_live_"+itoa(i), "web:multi", 1)
	}
	for i := 0; i < 3; i++ {
		id := "conv_dead_" + itoa(i)
		seedConv(t, s, id, "web:multi", 1)
		_ = s.DeleteConversation(context.Background(), id)
	}

	convs, total, err := s.ListConversationsPaginated(context.Background(), "web:multi", 20, 0)
	if err != nil {
		t.Fatalf("ListConversationsPaginated: %v", err)
	}
	if total != 5 {
		t.Errorf("total: got %d, want 5", total)
	}
	if len(convs) != 5 {
		t.Errorf("len(convs): got %d, want 5", len(convs))
	}
	for _, c := range convs {
		if c.ID == "conv_dead_0" || c.ID == "conv_dead_1" || c.ID == "conv_dead_2" {
			t.Errorf("soft-deleted conv leaked into list: %s", c.ID)
		}
	}
}

// --- B3. DeleteConversationsOlderThan ---

func TestDeleteConversationsOlderThan_RemovesExpired(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Three expired (deleted 31 days ago) + one recent (5 days ago).
	for i := 0; i < 3; i++ {
		id := "conv_exp_" + itoa(i)
		seedConv(t, s, id, "web:prune", 1)
		_, _ = s.db.ExecContext(ctx,
			`UPDATE conversations SET deleted_at = ? WHERE id = ?`,
			time.Now().UTC().AddDate(0, 0, -31), id)
	}
	seedConv(t, s, "conv_recent", "web:prune", 1)
	_, _ = s.db.ExecContext(ctx,
		`UPDATE conversations SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().AddDate(0, 0, -5), "conv_recent")

	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	n, err := s.DeleteConversationsOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteConversationsOlderThan: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted count: got %d, want 3", n)
	}

	// Recent one should still be in SQL.
	var cnt int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE id = ?`, "conv_recent").Scan(&cnt); err != nil {
		t.Fatalf("count conv_recent: %v", err)
	}
	if cnt != 1 {
		t.Errorf("conv_recent unexpectedly deleted; cnt=%d", cnt)
	}
}

func TestDeleteConversationsOlderThan_IgnoresLive(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_alive", "web:prune", 1)

	n, err := s.DeleteConversationsOlderThan(context.Background(),
		time.Now().UTC().Add(time.Hour)) // future cutoff, would prune everything matching
	if err != nil {
		t.Fatalf("DeleteConversationsOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("live conv should not be pruned; n=%d", n)
	}
}

// --- B4. GetConversationMessages paginated ---

func TestGetConversationMessages_InitialLoad(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_p", "web:pg", 200)

	msgs, hasMore, oldest, err := s.GetConversationMessages(context.Background(), "conv_p", -1, 50)
	if err != nil {
		t.Fatalf("GetConversationMessages: %v", err)
	}
	if len(msgs) != 50 {
		t.Errorf("len(msgs): got %d, want 50", len(msgs))
	}
	if oldest != 150 {
		t.Errorf("oldest: got %d, want 150", oldest)
	}
	if !hasMore {
		t.Errorf("hasMore: got false, want true")
	}
	if msgs[0].Content[0].Text != "msg-150" {
		t.Errorf("first msg text: got %q, want msg-150", msgs[0].Content[0].Text)
	}
}

func TestGetConversationMessages_PagingUpward(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_pg", "web:pg", 200)

	msgs, hasMore, oldest, err := s.GetConversationMessages(context.Background(), "conv_pg", 150, 50)
	if err != nil {
		t.Fatalf("GetConversationMessages: %v", err)
	}
	if len(msgs) != 50 {
		t.Errorf("len(msgs): got %d, want 50", len(msgs))
	}
	if oldest != 100 {
		t.Errorf("oldest: got %d, want 100", oldest)
	}
	if !hasMore {
		t.Errorf("hasMore: got false, want true")
	}
}

func TestGetConversationMessages_ReachingStart(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_start", "web:pg", 200)

	msgs, hasMore, oldest, err := s.GetConversationMessages(context.Background(), "conv_start", 50, 100)
	if err != nil {
		t.Fatalf("GetConversationMessages: %v", err)
	}
	if len(msgs) != 50 {
		t.Errorf("len(msgs): got %d, want 50 (bounded by start)", len(msgs))
	}
	if oldest != 0 {
		t.Errorf("oldest: got %d, want 0", oldest)
	}
	if hasMore {
		t.Errorf("hasMore: got true, want false")
	}
}

func TestGetConversationMessages_SoftDeletedReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_sd_msg", "web:pg", 5)
	_ = s.DeleteConversation(context.Background(), "conv_sd_msg")

	_, _, _, err := s.GetConversationMessages(context.Background(), "conv_sd_msg", -1, 50)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on soft-deleted conv, got %v", err)
	}
}

func TestGetConversationMessages_LimitClamping(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_clamp", "web:pg", 300)

	// limit=0 → 50
	msgs, _, _, _ := s.GetConversationMessages(context.Background(), "conv_clamp", -1, 0)
	if len(msgs) != 50 {
		t.Errorf("limit=0 should clamp to 50, got %d", len(msgs))
	}

	// limit=500 → 200
	msgs, _, _, _ = s.GetConversationMessages(context.Background(), "conv_clamp", -1, 500)
	if len(msgs) != 200 {
		t.Errorf("limit=500 should clamp to 200, got %d", len(msgs))
	}

	// limit=-3 → 50
	msgs, _, _, _ = s.GetConversationMessages(context.Background(), "conv_clamp", -1, -3)
	if len(msgs) != 50 {
		t.Errorf("limit=-3 should clamp to 50, got %d", len(msgs))
	}
}

func TestGetConversationMessages_EmptyConv(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_empty", "web:pg", 0)

	msgs, hasMore, oldest, err := s.GetConversationMessages(context.Background(), "conv_empty", -1, 50)
	if err != nil {
		t.Fatalf("GetConversationMessages on empty conv: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("len(msgs): got %d, want 0", len(msgs))
	}
	if hasMore {
		t.Errorf("hasMore: got true, want false")
	}
	if oldest != 0 {
		t.Errorf("oldest: got %d, want 0", oldest)
	}
}

func TestGetConversationMessages_DefensiveCopy(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_copy", "web:pg", 10)

	first, _, _, _ := s.GetConversationMessages(context.Background(), "conv_copy", -1, 5)
	first[0].Role = "MUTATED"

	second, _, _, _ := s.GetConversationMessages(context.Background(), "conv_copy", -1, 5)
	if second[0].Role == "MUTATED" {
		t.Errorf("defensive copy failed — mutation leaked back into store read")
	}
}

// --- B5. UpdateConversationTitle ---

func TestUpdateConversationTitle_Persists(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_t", "web:t", 1)

	if err := s.UpdateConversationTitle(context.Background(), "conv_t", "Mi nuevo hilo"); err != nil {
		t.Fatalf("UpdateConversationTitle: %v", err)
	}

	conv, err := s.LoadConversation(context.Background(), "conv_t")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if conv.Metadata["title"] != "Mi nuevo hilo" {
		t.Errorf("metadata.title: got %q, want %q", conv.Metadata["title"], "Mi nuevo hilo")
	}
}

func TestUpdateConversationTitle_EmptyRejected(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_empty_t", "web:t", 1)

	err := s.UpdateConversationTitle(context.Background(), "conv_empty_t", "   ")
	if !errors.Is(err, ErrInvalidTitle) {
		t.Errorf("expected ErrInvalidTitle, got %v", err)
	}
}

func TestUpdateConversationTitle_SoftDeletedReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	seedConv(t, s, "conv_sd_t", "web:t", 1)
	_ = s.DeleteConversation(context.Background(), "conv_sd_t")

	err := s.UpdateConversationTitle(context.Background(), "conv_sd_t", "x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateConversationTitle_NonexistentReturnsNotFound(t *testing.T) {
	s := newTestSQLiteStore(t)

	err := s.UpdateConversationTitle(context.Background(), "conv_ghost_t", "x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
