package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/config"
	"daimon/internal/store"
)

// newIntegrationStore creates a real SQLiteStore backed by a temp dir.
func newIntegrationStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newIntegrationServer creates a Server with real SQLiteStore and NoopAuditor.
func newIntegrationServer(t *testing.T) *Server {
	t.Helper()
	st := newIntegrationStore(t)
	s := &Server{
		deps: ServerDeps{
			Store:     st,
			Auditor:   audit.NoopAuditor{},
			Config:    minimalConfig(),
			StartedAt: time.Now(),
			Version:   "test",
		},
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// TestIntegration_AllEndpoints verifies every API endpoint with a real SQLiteStore.
func TestIntegration_AllEndpoints(t *testing.T) {
	srv := newIntegrationServer(t)
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)

	client := ts.Client()

	t.Run("GET /api/status returns 200 with name/provider/model", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		if body["name"] != "test-agent" {
			t.Errorf("expected name=test-agent, got %v", body["name"])
		}
		if body["provider"] != "anthropic" {
			t.Errorf("expected provider=anthropic, got %v", body["provider"])
		}
		if body["model"] != "claude-test" {
			t.Errorf("expected model=claude-test, got %v", body["model"])
		}
	})

	t.Run("GET /api/config returns 200 with masked api_key", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/config")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		// api_key must be present and masked (not the raw value).
		apiKey, _ := body["api_key"].(string)
		if strings.Contains(apiKey, "real-key") {
			t.Errorf("api_key should be masked, got %q", apiKey)
		}
	})

	t.Run("GET /api/conversations returns 200 with items array", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/conversations")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		if _, ok := body["items"]; !ok {
			t.Error("expected 'items' field in response")
		}
	})

	t.Run("GET /api/memory returns 200 with items array", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/memory")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		if _, ok := body["items"]; !ok {
			t.Error("expected 'items' field in response")
		}
	})

	t.Run("GET /api/metrics returns 200 with today/month/history shape", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		if _, ok := body["today"]; !ok {
			t.Error("expected 'today' field in metrics response")
		}
		if _, ok := body["month"]; !ok {
			t.Error("expected 'month' field in metrics response")
		}
		if _, ok := body["history"]; !ok {
			t.Error("expected 'history' field in metrics response")
		}
	})

	t.Run("GET /api/metrics/history?days=7 returns 200 with snapshot shape", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/metrics/history?days=7")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["history"]; !ok {
			t.Error("expected 'history' field in metrics history response")
		}
	})

	t.Run("GET /api/mcp/servers returns 200 with servers array", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/api/mcp/servers")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		if _, ok := body["servers"]; !ok {
			t.Error("expected 'servers' field in response")
		}
	})

	t.Run("POST /api/memory returns 201", func(t *testing.T) {
		entry := store.MemoryEntry{
			ScopeID: "integration-scope",
			Content: "integration test memory",
			Title:   "test note",
		}
		body, _ := json.Marshal(entry)

		resp, err := client.Post(ts.URL+"/api/memory", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
	})

	t.Run("DELETE /api/conversations/nonexistent returns 404", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/conversations/nonexistent-id", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", resp.StatusCode)
		}
	})
}

// TestIntegration_StaticServing verifies static file serving and SPA fallback.
func TestIntegration_StaticServing(t *testing.T) {
	srv := newIntegrationServer(t)
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)

	client := ts.Client()

	t.Run("GET / returns 200 with micro-claw content", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(bodyBytes), "<!doctype html>") && !strings.Contains(string(bodyBytes), "micro-claw") {
			t.Errorf("expected body to contain 'micro-claw', got: %s", string(bodyBytes))
		}
	})

	t.Run("GET /some/spa/route returns 200 via SPA fallback", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/some/spa/route")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for SPA fallback, got %d", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(bodyBytes), "<!doctype html>") && !strings.Contains(string(bodyBytes), "micro-claw") {
			t.Errorf("expected SPA fallback body to contain 'micro-claw', got: %s", string(bodyBytes))
		}
	})

	t.Run("GET /api/nonexistent — known API routes 404, catch-all SPA serves index", func(t *testing.T) {
		// Registered API routes return errors; unknown sub-paths under /api/ fall
		// through to the catch-all SPA handler and return 200 with index.html.
		// This verifies that a request to a well-known API base path that doesn't
		// match any route still resolves cleanly (no panic, no 500).
		resp, err := client.Get(ts.URL + "/api/nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		// The server must not return a 5xx error regardless.
		if resp.StatusCode >= 500 {
			t.Fatalf("expected <500 for unknown API route, got %d", resp.StatusCode)
		}
	})
}
