package modelcache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"daimon/internal/provider"
	"daimon/internal/web/modelcache"
)

// helpers

var errFetcher = errors.New("fetcher error")

func twoModels() []provider.ModelInfo {
	return []provider.ModelInfo{
		{ID: "model-a", Name: "Model A"},
		{ID: "model-b", Name: "Model B"},
	}
}

func successFetcher(models []provider.ModelInfo) modelcache.FetchFunc {
	return func(ctx context.Context) ([]provider.ModelInfo, error) {
		return models, nil
	}
}

func failFetcher() modelcache.FetchFunc {
	return func(ctx context.Context) ([]provider.ModelInfo, error) {
		return nil, errFetcher
	}
}

func countingFetcher(t *testing.T, models []provider.ModelInfo) (modelcache.FetchFunc, *int) {
	t.Helper()
	count := 0
	fn := func(ctx context.Context) ([]provider.ModelInfo, error) {
		count++
		return models, nil
	}
	return fn, &count
}

// 5.1.1 — cold miss then cache hit within TTL

func TestCache_GetOrFetch_ColdMissThenHit(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	c := modelcache.New(modelcache.Options{
		DefaultTTL: 10 * time.Minute,
		Clock:      clock,
	})

	fetcher, calls := countingFetcher(t, twoModels())

	// Cold miss — fetcher invoked.
	entry, err := c.GetOrFetch(context.Background(), "anthropic", fetcher, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "live" {
		t.Errorf("expected source=live, got %q", entry.Source)
	}
	if len(entry.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(entry.Models))
	}
	if *calls != 1 {
		t.Errorf("expected 1 fetcher call, got %d", *calls)
	}

	// Cache hit within TTL — fetcher NOT invoked again.
	entry2, err := c.GetOrFetch(context.Background(), "anthropic", fetcher, false)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if entry2.Source != "cache" {
		t.Errorf("expected source=cache, got %q", entry2.Source)
	}
	if *calls != 1 {
		t.Errorf("expected still 1 fetcher call, got %d", *calls)
	}
}

// 5.1.2 — expired TTL re-invokes fetcher

func TestCache_GetOrFetch_ExpiredTTL_RefetchesModels(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	c := modelcache.New(modelcache.Options{
		DefaultTTL: 5 * time.Minute,
		Clock:      clock,
	})

	fetcher, calls := countingFetcher(t, twoModels())

	// Populate cache.
	_, _ = c.GetOrFetch(context.Background(), "anthropic", fetcher, false)
	if *calls != 1 {
		t.Fatalf("pre-condition: expected 1 call, got %d", *calls)
	}

	// Advance clock past TTL.
	now = now.Add(6 * time.Minute)

	entry, err := c.GetOrFetch(context.Background(), "anthropic", fetcher, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "live" {
		t.Errorf("expected source=live after expiry, got %q", entry.Source)
	}
	if *calls != 2 {
		t.Errorf("expected 2 fetcher calls, got %d", *calls)
	}
}

// 5.1.3 — fetcher error with stale cache returns stale

func TestCache_GetOrFetch_FetcherError_StaleCache_ReturnsStale(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	c := modelcache.New(modelcache.Options{
		DefaultTTL: 5 * time.Minute,
		Clock:      clock,
	})

	// Populate cache first with a success fetcher.
	_, _ = c.GetOrFetch(context.Background(), "anthropic", successFetcher(twoModels()), false)

	// Advance clock so cache is stale.
	now = now.Add(6 * time.Minute)

	// Now fetcher fails — stale cache should be returned.
	entry, err := c.GetOrFetch(context.Background(), "anthropic", failFetcher(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "cache-stale" {
		t.Errorf("expected source=cache-stale, got %q", entry.Source)
	}
	if len(entry.Models) != 2 {
		t.Errorf("expected 2 stale models, got %d", len(entry.Models))
	}
}

// 5.1.4 — fetcher error with empty cache returns fallback from ProviderCatalog

func TestCache_GetOrFetch_FetcherError_EmptyCache_ReturnsFallback(t *testing.T) {
	c := modelcache.New(modelcache.Options{
		DefaultTTL: 5 * time.Minute,
		Clock:      time.Now,
	})

	entry, err := c.GetOrFetch(context.Background(), "anthropic", failFetcher(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "fallback" {
		t.Errorf("expected source=fallback, got %q", entry.Source)
	}
	// Fallback uses ProviderCatalog — should have anthropic models.
	if len(entry.Models) == 0 {
		t.Error("expected non-empty fallback models from ProviderCatalog")
	}

	// Fallback must NOT be stored in cache — next cold hit should still call fetcher.
	successFetch, calls := countingFetcher(t, twoModels())
	entry2, _ := c.GetOrFetch(context.Background(), "anthropic", successFetch, false)
	if *calls != 1 {
		t.Errorf("fallback was cached — expected fetcher call, got %d calls", *calls)
	}
	if entry2.Source != "live" {
		t.Errorf("expected live after fallback, got %q", entry2.Source)
	}
}

// 5.1.5 — refresh=true bypasses valid cache

func TestCache_GetOrFetch_RefreshBypassesCache(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }

	c := modelcache.New(modelcache.Options{
		DefaultTTL: 10 * time.Minute,
		Clock:      clock,
	})

	fetcher, calls := countingFetcher(t, twoModels())

	// Populate cache.
	_, _ = c.GetOrFetch(context.Background(), "anthropic", fetcher, false)
	if *calls != 1 {
		t.Fatalf("expected 1 call, got %d", *calls)
	}

	// Force refresh while cache is still valid.
	entry, err := c.GetOrFetch(context.Background(), "anthropic", fetcher, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "live" {
		t.Errorf("expected source=live on refresh, got %q", entry.Source)
	}
	if *calls != 2 {
		t.Errorf("expected 2 calls after refresh, got %d", *calls)
	}
}

// 5.1.6 — concurrent reads are safe (no data race)

func TestCache_GetOrFetch_ConcurrentSafe(t *testing.T) {
	c := modelcache.New(modelcache.Options{
		DefaultTTL: 10 * time.Minute,
		Clock:      time.Now,
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.GetOrFetch(context.Background(), "anthropic", successFetcher(twoModels()), false)
		}()
	}
	wg.Wait()
}

// test that unknown provider with fetcher error returns empty fallback (not catalog)

func TestCache_GetOrFetch_UnknownProvider_FetcherError_EmptyFallback(t *testing.T) {
	c := modelcache.New(modelcache.Options{
		DefaultTTL: 5 * time.Minute,
		Clock:      time.Now,
	})

	entry, err := c.GetOrFetch(context.Background(), "unknownxyz", failFetcher(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "fallback" {
		t.Errorf("expected source=fallback for unknown provider, got %q", entry.Source)
	}
	// Unknown provider has no catalog — fallback should be empty models.
	if len(entry.Models) != 0 {
		t.Errorf("expected empty fallback models for unknown provider, got %d", len(entry.Models))
	}
}
