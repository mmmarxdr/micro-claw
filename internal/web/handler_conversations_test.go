package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// fakeWebStore implements both store.Store and store.WebStore for testing.
type fakeWebStore struct {
	conversations []store.Conversation
	memory        []store.MemoryEntry
}

func (f *fakeWebStore) SaveConversation(_ context.Context, conv store.Conversation) error {
	f.conversations = append(f.conversations, conv)
	return nil
}

func (f *fakeWebStore) LoadConversation(_ context.Context, id string) (*store.Conversation, error) {
	for i := range f.conversations {
		if f.conversations[i].ID == id {
			return &f.conversations[i], nil
		}
	}

	return nil, store.ErrNotFound
}

func (f *fakeWebStore) ListConversations(_ context.Context, _ string, limit int) ([]store.Conversation, error) {
	if limit > len(f.conversations) {
		limit = len(f.conversations)
	}

	return f.conversations[:limit], nil
}

func (f *fakeWebStore) AppendMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	f.memory = append(f.memory, entry)
	return nil
}

func (f *fakeWebStore) SearchMemory(_ context.Context, _ string, _ string, limit int) ([]store.MemoryEntry, error) {
	if limit <= 0 || limit > len(f.memory) {
		limit = len(f.memory)
	}

	return f.memory[:limit], nil
}

func (f *fakeWebStore) UpdateMemory(_ context.Context, _ string, _ store.MemoryEntry) error {
	return nil
}

func (f *fakeWebStore) Close() error { return nil }

// WebStore extension methods.
func (f *fakeWebStore) ListConversationsPaginated(_ context.Context, _ string, limit, offset int) ([]store.Conversation, int, error) {
	total := len(f.conversations)
	if offset >= total {
		return []store.Conversation{}, total, nil
	}

	end := offset + limit
	if end > total {
		end = total
	}

	return f.conversations[offset:end], total, nil
}

func (f *fakeWebStore) CountConversations(_ context.Context, _ string) (int, error) {
	return len(f.conversations), nil
}

func (f *fakeWebStore) DeleteConversation(_ context.Context, id string) error {
	for i, c := range f.conversations {
		if c.ID == id {
			f.conversations = append(f.conversations[:i], f.conversations[i+1:]...)
			return nil
		}
	}

	return store.ErrNotFound
}

func (f *fakeWebStore) DeleteMemory(_ context.Context, _ string, _ int64) error {
	return store.ErrNotFound
}

// --- WebStore extensions added by conversations-liminal-resume Group B ---

func (f *fakeWebStore) RestoreConversation(_ context.Context, _ string) error {
	return store.ErrNotFound
}

func (f *fakeWebStore) DeleteConversationsOlderThan(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

func (f *fakeWebStore) GetConversationMessages(_ context.Context, id string, beforeIndex, limit int) ([]provider.ChatMessage, bool, int, error) {
	for _, c := range f.conversations {
		if c.ID == id {
			if limit <= 0 {
				limit = 50
			}
			if limit > 200 {
				limit = 200
			}
			total := len(c.Messages)
			end := total
			if beforeIndex >= 0 && beforeIndex < total {
				end = beforeIndex
			}
			if end <= 0 {
				return []provider.ChatMessage{}, false, 0, nil
			}
			start := end - limit
			if start < 0 {
				start = 0
			}
			out := make([]provider.ChatMessage, end-start)
			copy(out, c.Messages[start:end])
			return out, start > 0, start, nil
		}
	}
	return nil, false, 0, store.ErrNotFound
}

func (f *fakeWebStore) UpdateConversationTitle(_ context.Context, id, title string) error {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return store.ErrInvalidTitle
	}
	for i, c := range f.conversations {
		if c.ID == id {
			if c.Metadata == nil {
				c.Metadata = map[string]string{}
			}
			c.Metadata["title"] = trimmed
			f.conversations[i] = c
			return nil
		}
	}
	return store.ErrNotFound
}

// noWebStore — a store.Store that does NOT implement WebStore.
type noWebStore struct{}

func (noWebStore) SaveConversation(_ context.Context, _ store.Conversation) error { return nil }
func (noWebStore) LoadConversation(_ context.Context, _ string) (*store.Conversation, error) {
	return nil, store.ErrNotFound
}

func (noWebStore) ListConversations(_ context.Context, _ string, _ int) ([]store.Conversation, error) {
	return nil, nil
}
func (noWebStore) AppendMemory(_ context.Context, _ string, _ store.MemoryEntry) error { return nil }
func (noWebStore) SearchMemory(_ context.Context, _ string, _ string, _ int) ([]store.MemoryEntry, error) {
	return nil, nil
}
func (noWebStore) UpdateMemory(_ context.Context, _ string, _ store.MemoryEntry) error { return nil }
func (noWebStore) Close() error                                                        { return nil }

func newTestServerWithStore(t *testing.T, st store.Store) *Server {
	t.Helper()

	s := &Server{
		deps: ServerDeps{
			Store:     st,
			StartedAt: time.Now(),
			Config:    minimalConfig(),
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	return s
}

func makeConversation(id, channelID string, msgs ...string) store.Conversation {
	c := store.Conversation{
		ID:        id,
		ChannelID: channelID,
		UpdatedAt: time.Now(),
	}
	for _, txt := range msgs {
		c.Messages = append(c.Messages, provider.ChatMessage{
			Role: "user",
			Content: content.Blocks{
				{Type: content.BlockText, Text: txt},
			},
		})
	}

	return c
}

func TestHandleListConversations_returnsItems(t *testing.T) {
	fs := &fakeWebStore{
		conversations: []store.Conversation{
			makeConversation("c1", "ch1", "hello"),
			makeConversation("c2", "ch2", "world"),
		},
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	items, _ := resp["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	total, _ := resp["total"].(float64)
	if int(total) != 2 {
		t.Fatalf("expected total=2, got %v", total)
	}
}

func TestHandleListConversations_pagination(t *testing.T) {
	fs := &fakeWebStore{}
	for i := range 5 {
		fs.conversations = append(fs.conversations, makeConversation(
			"c"+string(rune('0'+i)), "ch1",
		))
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/conversations?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	items, _ := resp["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 items (page), got %d", len(items))
	}

	total, _ := resp["total"].(float64)
	if int(total) != 5 {
		t.Fatalf("expected total=5, got %v", total)
	}
}

func TestHandleListConversations_noWebStore_returns501(t *testing.T) {
	srv := newTestServerWithStore(t, noWebStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestHandleGetConversation_found(t *testing.T) {
	fs := &fakeWebStore{
		conversations: []store.Conversation{makeConversation("abc", "ch1", "hi")},
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/conversations/abc", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGetConversation_notFound(t *testing.T) {
	fs := &fakeWebStore{}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/conversations/missing", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleDeleteConversation_ok(t *testing.T) {
	fs := &fakeWebStore{
		conversations: []store.Conversation{makeConversation("del1", "ch1")},
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/del1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleDeleteConversation_notFound(t *testing.T) {
	fs := &fakeWebStore{}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/nope", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleDeleteConversation_noWebStore_returns501(t *testing.T) {
	srv := newTestServerWithStore(t, noWebStore{})

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/x", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}
