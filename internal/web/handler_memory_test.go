package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"daimon/internal/store"
)

func TestHandleListMemory_returnsItems(t *testing.T) {
	fs := &fakeWebStore{
		memory: []store.MemoryEntry{
			{ID: "1", ScopeID: "s1", Content: "remember this"},
			{ID: "2", ScopeID: "s1", Content: "and this"},
		},
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
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
}

func TestHandleListMemory_limitParam(t *testing.T) {
	fs := &fakeWebStore{}
	for i := range 10 {
		fs.memory = append(fs.memory, store.MemoryEntry{
			ID: string(rune('a' + i)), ScopeID: "s", Content: "x",
		})
	}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodGet, "/api/memory?limit=3", nil)
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
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
}

func TestHandlePostMemory_ok(t *testing.T) {
	fs := &fakeWebStore{}
	srv := newTestServerWithStore(t, fs)

	entry := store.MemoryEntry{
		ScopeID: "scope1",
		Content: "test memory",
		Title:   "my note",
	}
	body, _ := json.Marshal(entry)

	req := httptest.NewRequest(http.MethodPost, "/api/memory", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	if len(fs.memory) != 1 {
		t.Fatalf("expected 1 entry in store, got %d", len(fs.memory))
	}
}

func TestHandlePostMemory_badJSON(t *testing.T) {
	fs := &fakeWebStore{}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodPost, "/api/memory", bytes.NewBufferString("not json"))
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleDeleteMemory_noWebStore_returns501(t *testing.T) {
	srv := newTestServerWithStore(t, noWebStore{})

	req := httptest.NewRequest(http.MethodDelete, "/api/memory/1", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", w.Code)
	}
}

func TestHandleDeleteMemory_notFound(t *testing.T) {
	fs := &fakeWebStore{}
	srv := newTestServerWithStore(t, fs)

	req := httptest.NewRequest(http.MethodDelete, "/api/memory/999", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
