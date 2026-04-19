package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 8.2 — regression test: GET /api/models no longer serves a JSON model list.
// The SPA catch-all returns 200+HTML for all unmatched paths, so we assert that
// the response body is NOT a JSON array (which the old handler always returned).
func TestLegacyModelsEndpointGone(t *testing.T) {
	cfg := minimalConfig()
	s := NewServer(ServerDeps{Config: cfg})

	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// The old handler returned a JSON array. If /api/models is gone, the
	// SPA catch-all handles it and Content-Type is NOT application/json.
	ct := w.Header().Get("Content-Type")
	if ct == "application/json" {
		t.Errorf("/api/models returned Content-Type application/json — route still exists")
	}

	// Also assert the body is not a valid JSON array of models.
	var models []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&models); err == nil {
		t.Errorf("/api/models returned a valid JSON array — legacy endpoint was not deleted")
	}
}
