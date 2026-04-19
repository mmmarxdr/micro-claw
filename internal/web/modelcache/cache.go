// Package modelcache provides a TTL-based, per-provider, thread-safe model list cache.
// When the live fetcher fails, the cache falls back to a stale entry or the offline
// ProviderCatalog from internal/setup. The fallback is never stored in cache so that
// the next request can try the live fetcher again.
package modelcache

import (
	"context"
	"sync"
	"time"

	"daimon/internal/provider"
	"daimon/internal/setup"
)

// FetchFunc fetches a live model list for the caller-specified provider.
type FetchFunc func(ctx context.Context) ([]provider.ModelInfo, error)

// Entry is the result of a GetOrFetch call.
type Entry struct {
	Models   []provider.ModelInfo
	Source   string    // "live" | "cache" | "cache-stale" | "fallback"
	CachedAt time.Time // time the live data was stored (zero for fallback)
}

// cacheEntry is the internal store entry, always representing live data.
type cacheEntry struct {
	models   []provider.ModelInfo
	cachedAt time.Time
}

// Options configure the cache at construction time.
type Options struct {
	// DefaultTTL is used for all providers unless overridden by PerProviderTTL.
	DefaultTTL time.Duration
	// PerProviderTTL optionally overrides TTL per provider name.
	PerProviderTTL map[string]time.Duration
	// Clock is injectable for testing (defaults to time.Now).
	Clock func() time.Time
}

// Cache is a thread-safe, TTL-keyed model list cache.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	opts    Options
}

// DefaultPerProviderTTL returns the built-in per-provider TTL seeds.
// anthropic/openai/openrouter/gemini=10min, ollama=5min.
func DefaultPerProviderTTL() map[string]time.Duration {
	return map[string]time.Duration{
		"anthropic":  10 * time.Minute,
		"openai":     10 * time.Minute,
		"openrouter": 10 * time.Minute,
		"gemini":     10 * time.Minute,
		"ollama":     5 * time.Minute,
	}
}

// New creates a new Cache.
// When Options.PerProviderTTL is nil AND Options.DefaultTTL is zero (zero-value Options),
// the DefaultPerProviderTTL seeds are applied and DefaultTTL is set to 10 min.
// When the caller explicitly provides DefaultTTL, PerProviderTTL is not seeded unless
// the caller also provides it — this allows tests to set a uniform TTL easily.
func New(opts Options) *Cache {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	// Only seed per-provider defaults when the caller has not set any TTL at all.
	if opts.DefaultTTL == 0 && opts.PerProviderTTL == nil {
		opts.DefaultTTL = 10 * time.Minute
		opts.PerProviderTTL = DefaultPerProviderTTL()
	} else if opts.DefaultTTL == 0 {
		opts.DefaultTTL = 10 * time.Minute
	}
	if opts.PerProviderTTL == nil {
		opts.PerProviderTTL = make(map[string]time.Duration)
	}
	return &Cache{
		entries: make(map[string]cacheEntry),
		opts:    opts,
	}
}

// ttlFor returns the effective TTL for a provider name.
func (c *Cache) ttlFor(providerName string) time.Duration {
	if ttl, ok := c.opts.PerProviderTTL[providerName]; ok {
		return ttl
	}
	return c.opts.DefaultTTL
}

// GetOrFetch returns models for providerName.
// - If refresh is false and the cache entry is fresh, returns source="cache".
// - If the cache is missing or stale, calls fetcher.
//   - On success: stores in cache, returns source="live".
//   - On error + stale cache: returns stale data, source="cache-stale".
//   - On error + empty cache: returns ProviderCatalog data, source="fallback".
//     Fallback is NOT stored in cache.
func (c *Cache) GetOrFetch(ctx context.Context, providerName string, fetcher FetchFunc, refresh bool) (Entry, error) {
	now := c.opts.Clock()
	ttl := c.ttlFor(providerName)

	if !refresh {
		// Fast path: RLock and check for a fresh entry.
		c.mu.RLock()
		e, ok := c.entries[providerName]
		c.mu.RUnlock()

		if ok && now.Sub(e.cachedAt) < ttl {
			return Entry{
				Models:   e.models,
				Source:   "cache",
				CachedAt: e.cachedAt,
			}, nil
		}
	}

	// Cache miss, stale, or forced refresh — call fetcher.
	models, err := fetcher(ctx)
	if err == nil {
		// Store fresh data.
		c.mu.Lock()
		c.entries[providerName] = cacheEntry{models: models, cachedAt: now}
		c.mu.Unlock()
		return Entry{
			Models:   models,
			Source:   "live",
			CachedAt: now,
		}, nil
	}

	// Fetcher failed — try stale cache.
	c.mu.RLock()
	e, ok := c.entries[providerName]
	c.mu.RUnlock()

	if ok {
		return Entry{
			Models:   e.models,
			Source:   "cache-stale",
			CachedAt: e.cachedAt,
		}, nil
	}

	// No cache at all — synthesise fallback from ProviderCatalog.
	// Convert setup.ModelInfo to provider.ModelInfo for type compatibility.
	catalogEntries := setup.ModelsForProvider(providerName)
	var fallbackModels []provider.ModelInfo
	for _, m := range catalogEntries {
		fallbackModels = append(fallbackModels, provider.ModelInfo{
			ID:   m.ID,
			Name: m.DisplayName,
		})
	}
	// Fallback NOT stored in cache.
	return Entry{
		Models: fallbackModels,
		Source: "fallback",
	}, nil
}
