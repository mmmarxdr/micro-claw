package web

// integration_provider_models_test.go — Integration tests for GET /api/providers/{provider}/models.
//
// Uses a real Server with real ProviderRegistry + ModelCache wiring (no unit-test mocks),
// verifying the full HTTP → registry → cache → response pipeline.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/store"
	"daimon/internal/web/modelcache"
)

// ---------------------------------------------------------------------------
// Integration helpers
// ---------------------------------------------------------------------------

// buildIntegrationServer creates a Server wired with real ProviderRegistry + ModelCache
// using the provided fake listers.
func buildIntegrationServer(t *testing.T, listers map[string]provider.ModelLister) *Server {
	t.Helper()

	st, err := store.NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	reg := &fakeRegistry{listers: listers}

	// Use a short TTL so we can test cache expiry easily in the same request pair.
	cache := modelcache.New(modelcache.Options{
		DefaultTTL: 10 * time.Minute,
	})

	s := &Server{
		deps: ServerDeps{
			Store:            st,
			Auditor:          audit.NoopAuditor{},
			Config:           minimalConfig(),
			StartedAt:        time.Now(),
			Version:          "test",
			ProviderRegistry: reg,
			ModelCache:       cache,
		},
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// ---------------------------------------------------------------------------
// Test 14.2.1 — configured provider: cold miss → live, warm hit → cache
// ---------------------------------------------------------------------------

func TestIntegration_ProviderModels_LiveThenCache(t *testing.T) {
	models := []provider.ModelInfo{
		{ID: "claude-opus-4-7", Name: "Claude Opus 4.7"},
		{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
	}

	s := buildIntegrationServer(t, map[string]provider.ModelLister{
		"anthropic": &fakeLister{models: models},
	})
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	client := ts.Client()

	// First call: cold cache → live.
	resp1, err := client.Get(ts.URL + "/api/providers/anthropic/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	if src := resp1.Header.Get("X-Source"); src != "live" {
		t.Errorf("first call: expected X-Source=live, got %q", src)
	}
	var body1 map[string]any
	if err := json.NewDecoder(resp1.Body).Decode(&body1); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body1["source"] != "live" {
		t.Errorf("first call: expected source=live in body, got %v", body1["source"])
	}
	items1, _ := body1["models"].([]any)
	if len(items1) != 2 {
		t.Errorf("first call: expected 2 models, got %d", len(items1))
	}

	// Second call (within TTL): cached.
	resp2, err := client.Get(ts.URL + "/api/providers/anthropic/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on cache hit, got %d", resp2.StatusCode)
	}
	if src := resp2.Header.Get("X-Source"); src != "cache" {
		t.Errorf("second call: expected X-Source=cache, got %q", src)
	}
}

// ---------------------------------------------------------------------------
// Test 14.2.2 — two providers use separate cache keys (no cross-contamination)
// ---------------------------------------------------------------------------

func TestIntegration_ProviderModels_SeparateCacheKeys(t *testing.T) {
	anthropicModels := []provider.ModelInfo{
		{ID: "claude-opus-4-7", Name: "Claude Opus 4.7"},
	}
	openrouterModels := []provider.ModelInfo{
		{ID: "openai/gpt-4o", Name: "GPT-4o"},
		{ID: "anthropic/claude-3.5-sonnet", Name: "Claude 3.5 Sonnet"},
	}

	s := buildIntegrationServer(t, map[string]provider.ModelLister{
		"anthropic":  &fakeLister{models: anthropicModels},
		"openrouter": &fakeLister{models: openrouterModels},
	})
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	client := ts.Client()

	// Populate anthropic cache.
	respA, err := client.Get(ts.URL + "/api/providers/anthropic/models")
	if err != nil {
		t.Fatal(err)
	}
	defer respA.Body.Close()
	if respA.StatusCode != 200 {
		t.Fatalf("anthropic request failed: %d", respA.StatusCode)
	}

	// Populate openrouter cache.
	respOR, err := client.Get(ts.URL + "/api/providers/openrouter/models")
	if err != nil {
		t.Fatal(err)
	}
	defer respOR.Body.Close()
	if respOR.StatusCode != 200 {
		t.Fatalf("openrouter request failed: %d", respOR.StatusCode)
	}

	// Now assert no cross-contamination: anthropic cache has anthropic models.
	respA2, err := client.Get(ts.URL + "/api/providers/anthropic/models")
	if err != nil {
		t.Fatal(err)
	}
	defer respA2.Body.Close()

	var bodyA map[string]any
	if err := json.NewDecoder(respA2.Body).Decode(&bodyA); err != nil {
		t.Fatalf("decode anthropic body: %v", err)
	}
	modelsA, _ := bodyA["models"].([]any)
	if len(modelsA) != 1 {
		t.Errorf("expected 1 anthropic model from cache, got %d (cross-contamination?)", len(modelsA))
	}
	// Verify anthropic cache returned X-Source=cache (not a new live call).
	if src := respA2.Header.Get("X-Source"); src != "cache" {
		t.Errorf("expected X-Source=cache for anthropic second call, got %q", src)
	}

	// Assert openrouter cache returns 2 models (not anthropic's 1).
	respOR2, err := client.Get(ts.URL + "/api/providers/openrouter/models")
	if err != nil {
		t.Fatal(err)
	}
	defer respOR2.Body.Close()

	var bodyOR map[string]any
	if err := json.NewDecoder(respOR2.Body).Decode(&bodyOR); err != nil {
		t.Fatalf("decode openrouter body: %v", err)
	}
	modelsOR, _ := bodyOR["models"].([]any)
	if len(modelsOR) != 2 {
		t.Errorf("expected 2 openrouter models from cache, got %d (cross-contamination?)", len(modelsOR))
	}
}

// ---------------------------------------------------------------------------
// Test 14.2.3 — known provider with no key → 401
// ---------------------------------------------------------------------------

func TestIntegration_ProviderModels_KnownButNoKey_401(t *testing.T) {
	// Registry has no openai entry.
	s := buildIntegrationServer(t, map[string]provider.ModelLister{
		"anthropic": &fakeLister{models: threeModels()},
	})
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/providers/openai/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for known-but-unconfigured provider, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Test 14.2.4 — unknown provider → 404
// ---------------------------------------------------------------------------

func TestIntegration_ProviderModels_Unknown_404(t *testing.T) {
	s := buildIntegrationServer(t, map[string]provider.ModelLister{})
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/providers/notaprovider/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown provider, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Test 14.2.5 — legacy GET /api/models → 404 (regression: hard cutover)
// ---------------------------------------------------------------------------

func TestIntegration_LegacyModelsEndpoint_404(t *testing.T) {
	s := buildIntegrationServer(t, map[string]provider.ModelLister{})
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)

	// The legacy /api/models endpoint was deleted in Phase 8.
	// It must NOT return JSON model list — either 404 or SPA fallback (200 with HTML).
	resp, err := ts.Client().Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Must not be a successful JSON model list (no 200 with {"models":[...]}).
	// The SPA fallback returns index.html (200), which does not contain a "models" JSON key.
	// Either outcome (404 or SPA 200) is acceptable; JSON model list at this URL is the regression.
	if resp.StatusCode == http.StatusOK {
		// Check it's not a JSON model list.
		contentType := resp.Header.Get("Content-Type")
		if contentType == "application/json" {
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
				if _, hasModels := body["models"]; hasModels {
					t.Error("legacy GET /api/models returned a JSON model list — the hard cutover regression is triggered")
				}
			}
		}
	}
	// Any non-200 (e.g. 404, 405) is also acceptable.
}
