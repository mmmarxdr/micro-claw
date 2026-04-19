package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/web/modelcache"
)

// providerModelsResponse is the JSON body for GET /api/providers/{provider}/models.
type providerModelsResponse struct {
	Models   []provider.ModelInfo `json:"models"`
	Source   string               `json:"source"`
	CachedAt *time.Time           `json:"cached_at,omitempty"`
}

// modelCacheFor returns the server's model cache, creating a default one if nil.
// The default uses the built-in per-provider TTLs.
func (s *Server) modelCacheFor() *modelcache.Cache {
	if s.deps.ModelCache != nil {
		return s.deps.ModelCache
	}
	// Lazy-init a default cache (not stored back — server is immutable after construction).
	// This path only triggers in tests or legacy setups without a wired cache.
	return modelcache.New(modelcache.Options{})
}

// handleListProviderModels handles GET /api/providers/{provider}/models.
//
// Response envelope:
//
//	200 + X-Source: live        — freshly fetched from provider
//	200 + X-Source: cache       — served from in-memory cache within TTL
//	200 + X-Source: cache-stale — fetcher failed, returned stale cached data
//	200 + X-Source: fallback    — fetcher failed, no cache, using ProviderCatalog
//	404                         — provider name not registered in registry
func (s *Server) handleListProviderModels(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")

	reg := s.deps.ProviderRegistry
	if reg == nil {
		http.Error(w, `{"error":"provider registry not available"}`, http.StatusServiceUnavailable)
		return
	}

	// Check KnownProviders first:
	//   - Not in KnownProviders → 404 (invalid name)
	//   - In KnownProviders but not in registry → 401 (valid name, no API key configured)
	if !config.IsKnownProvider(providerName) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "unknown provider: " + providerName,
		})
		return
	}

	lister, ok := reg.Lister(providerName)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("provider %s: no API key configured", providerName),
		})
		return
	}

	refresh := r.URL.Query().Get("refresh") == "true"
	cache := s.modelCacheFor()

	fetchFn := func(ctx context.Context) ([]provider.ModelInfo, error) {
		return lister.ListModels(ctx)
	}

	entry, err := cache.GetOrFetch(r.Context(), providerName, fetchFn, refresh)
	if err != nil {
		slog.Error("handleListProviderModels: unexpected cache error",
			"provider", providerName, "error", err)
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Source", entry.Source)

	resp := providerModelsResponse{
		Models: entry.Models,
		Source: entry.Source,
	}
	if !entry.CachedAt.IsZero() {
		t := entry.CachedAt
		resp.CachedAt = &t
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("handleListProviderModels: encode error", "error", err)
	}
}
