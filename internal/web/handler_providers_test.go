package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daimon/internal/provider"
	"daimon/internal/web/modelcache"
)

// fakeRegistry is a test-only Registry stand-in.
type fakeRegistry struct {
	listers map[string]provider.ModelLister
}

func (f *fakeRegistry) Lister(name string) (provider.ModelLister, bool) {
	ml, ok := f.listers[name]
	return ml, ok
}

func (f *fakeRegistry) RegisterTransient(name string, p provider.Provider) {
	if ml, ok := p.(provider.ModelLister); ok {
		f.listers[name] = ml
	}
}

// fakeLister provides canned model responses for tests.
type fakeLister struct {
	models []provider.ModelInfo
	err    error
}

func (f *fakeLister) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	return f.models, f.err
}

var errFetchFailed = errors.New("provider unreachable")

func threeModels() []provider.ModelInfo {
	return []provider.ModelInfo{
		{ID: "m1", Name: "Model 1"},
		{ID: "m2", Name: "Model 2"},
		{ID: "m3", Name: "Model 3"},
	}
}

func buildProviderServer(t *testing.T, reg providerRegistry, cache *modelcache.Cache) *Server {
	t.Helper()
	cfg := minimalConfig()
	s := &Server{
		deps: ServerDeps{
			Config:           cfg,
			ProviderRegistry: reg,
			ModelCache:       cache,
		},
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// freshCache with default options (no per-provider overrides, 10 min TTL).
func freshCache() *modelcache.Cache {
	return modelcache.New(modelcache.Options{
		DefaultTTL: 10 * time.Minute,
	})
}

// 7.1.1 — cold cache, live fetch succeeds → 200, X-Source: live

func TestProviderModels_ColdCache_LiveFetch_200(t *testing.T) {
	reg := &fakeRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeLister{models: threeModels()},
		},
	}

	s := buildProviderServer(t, reg, freshCache())

	req := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if src := w.Header().Get("X-Source"); src != "live" {
		t.Errorf("expected X-Source=live, got %q", src)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	models, ok := resp["models"].([]any)
	if !ok || len(models) != 3 {
		t.Errorf("expected 3 models in body, got: %v", resp["models"])
	}
	if resp["source"] != "live" {
		t.Errorf("expected source=live in body, got %v", resp["source"])
	}
}

// 7.1.2 — cache populated → 200, X-Source: cache

func TestProviderModels_CacheHit_200(t *testing.T) {
	reg := &fakeRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeLister{models: threeModels()},
		},
	}

	cache := freshCache()
	s := buildProviderServer(t, reg, cache)

	// First request populates cache.
	req1 := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w1 := httptest.NewRecorder()
	s.mux.ServeHTTP(w1, req1)
	if w1.Code != 200 {
		t.Fatalf("first request failed: %d", w1.Code)
	}

	// Second request should hit cache.
	req2 := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	if src := w2.Header().Get("X-Source"); src != "cache" {
		t.Errorf("expected X-Source=cache, got %q", src)
	}
}

// 7.1.3 — fetcher errors, stale cache present → 200, X-Source: cache-stale

func TestProviderModels_FetcherError_StaleCache_CacheStale(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	cache := modelcache.New(modelcache.Options{
		DefaultTTL: 1 * time.Minute,
		Clock:      clock,
	})

	// Populate cache with a success fetcher.
	goodLister := &fakeLister{models: threeModels()}
	reg := &fakeRegistry{
		listers: map[string]provider.ModelLister{"anthropic": goodLister},
	}
	s := buildProviderServer(t, reg, cache)

	// First call to populate.
	req := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("setup request failed: %d", w.Code)
	}

	// Advance time past TTL and swap lister to an erroring one.
	now = now.Add(5 * time.Minute)
	reg.listers["anthropic"] = &fakeLister{err: errFetchFailed}

	req2 := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 for stale, got %d", w2.Code)
	}
	if src := w2.Header().Get("X-Source"); src != "cache-stale" {
		t.Errorf("expected X-Source=cache-stale, got %q", src)
	}
}

// 7.1.4 — fetcher errors, no cache → 200, X-Source: fallback

func TestProviderModels_FetcherError_NoCache_Fallback(t *testing.T) {
	reg := &fakeRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeLister{err: errFetchFailed},
		},
	}

	s := buildProviderServer(t, reg, freshCache())

	req := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for fallback, got %d", w.Code)
	}
	if src := w.Header().Get("X-Source"); src != "fallback" {
		t.Errorf("expected X-Source=fallback, got %q", src)
	}
}

// 7.1.5 — provider not in registry → 404

func TestProviderModels_UnknownProvider_404(t *testing.T) {
	reg := &fakeRegistry{listers: map[string]provider.ModelLister{}}

	s := buildProviderServer(t, reg, freshCache())

	req := httptest.NewRequest(http.MethodGet, "/api/providers/badprovider/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown provider, got %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected non-empty error message in body")
	}
}

// 7.1.6 — ?refresh=true forces new fetch even with valid cache

func TestProviderModels_RefreshParam_ForcesFetch(t *testing.T) {
	callCount := 0
	lister := &countingLister{
		models:    threeModels(),
		callCount: &callCount,
	}
	reg := &fakeRegistry{
		listers: map[string]provider.ModelLister{"anthropic": lister},
	}

	cache := freshCache()
	s := buildProviderServer(t, reg, cache)

	// First request to populate cache.
	req1 := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w1 := httptest.NewRecorder()
	s.mux.ServeHTTP(w1, req1)
	if callCount != 1 {
		t.Fatalf("expected 1 call after first request, got %d", callCount)
	}

	// Request with ?refresh=true should call fetcher again.
	req2 := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models?refresh=true", nil)
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	if src := w2.Header().Get("X-Source"); src != "live" {
		t.Errorf("expected X-Source=live on refresh, got %q", src)
	}
	if callCount != 2 {
		t.Errorf("expected 2 fetcher calls after refresh, got %d", callCount)
	}
}

// deferred fix — 401 vs 404 semantics
// Known provider (in config.KnownProviders) but not in registry → 401.

func TestProviderModels_KnownProviderNotConfigured_401(t *testing.T) {
	// anthropic is a known provider but has no credentials/registry entry.
	reg := &fakeRegistry{listers: map[string]provider.ModelLister{}}

	s := buildProviderServer(t, reg, freshCache())

	req := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for known provider with no credentials, got %d", w.Code)
	}
	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	// REQ-PMD-2: body MUST be exactly `{"error": "provider {name}: no API key configured"}`
	want := "provider anthropic: no API key configured"
	if resp["error"] != want {
		t.Errorf("error body drift from REQ-PMD-2\n  want: %q\n  got:  %q", want, resp["error"])
	}
}

// Unknown provider (not in config.KnownProviders) → 404 (unchanged).

func TestProviderModels_UnknownProvider_StillReturns404(t *testing.T) {
	reg := &fakeRegistry{listers: map[string]provider.ModelLister{}}
	s := buildProviderServer(t, reg, freshCache())

	req := httptest.NewRequest(http.MethodGet, "/api/providers/notaprovider/models", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown provider, got %d", w.Code)
	}
}

// countingLister counts how many times ListModels is called.
type countingLister struct {
	models    []provider.ModelInfo
	callCount *int
}

func (c *countingLister) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	*c.callCount++
	return c.models, nil
}
