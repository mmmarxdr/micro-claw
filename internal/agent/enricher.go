package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// enrichJob represents a single tag-enrichment request queued for async processing.
type enrichJob struct {
	entry store.MemoryEntry
}

// Enricher asynchronously generates LLM-based concept tags for new memory entries.
// It runs a single worker goroutine draining a bounded channel.
type Enricher struct {
	store    store.Store
	provider provider.Provider
	model    string        // resolved enrichment model (cheap)
	ch       chan enrichJob // buffered channel, capacity 5
	limiter  *rateLimiter  // sliding-window rate limiter
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewEnricher creates a new Enricher and starts its worker goroutine.
// Returns nil if cfg.EnrichMemory is false.
// The caller must call Stop() when done and ensure run() is started via a goroutine.
func NewEnricher(prov provider.Provider, st store.Store, cfg config.AgentConfig) *Enricher {
	if !cfg.EnrichMemory {
		return nil
	}

	ratePerMin := cfg.EnrichRatePerMin
	if ratePerMin <= 0 {
		ratePerMin = 10
	}

	return &Enricher{
		store:    st,
		provider: prov,
		model:    resolveEnrichModel(prov, cfg.EnrichModel),
		ch:       make(chan enrichJob, 5),
		limiter:  newRateLimiter(ratePerMin, time.Minute),
	}
}

// Enqueue submits a memory entry for async tag enrichment.
// Non-blocking: if the channel is full or already stopped, the job is silently dropped.
func (e *Enricher) Enqueue(entry store.MemoryEntry) {
	defer func() {
		// Guard against send on closed channel if Stop() races with Enqueue.
		recover() //nolint:errcheck
	}()
	select {
	case e.ch <- enrichJob{entry: entry}:
	default:
		slog.Debug("enricher: channel full, dropping job", "entry_id", entry.ID)
	}
}

// Start launches the worker goroutine. Must be called once after NewEnricher.
// The goroutine exits when ctx is cancelled or Stop() is called.
func (e *Enricher) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.run(ctx)
}

// run is the internal worker loop.
func (e *Enricher) run(ctx context.Context) {
	defer e.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-e.ch:
			if !ok {
				return
			}
			if !e.limiter.Allow() {
				slog.Debug("enricher: rate limit reached, skipping job", "entry_id", job.entry.ID)
				continue
			}
			e.processJob(ctx, job)
		}
	}
}

// processJob performs the LLM call to extract tags and updates the store.
func (e *Enricher) processJob(ctx context.Context, job enrichJob) {
	jobCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	prompt := fmt.Sprintf(
		"Extract 3-5 concept tags from this text. Return ONLY comma-separated tags, no explanation.\nText: %s",
		job.entry.Content,
	)

	req := provider.ChatRequest{
		Model:        e.model, // use cheap model for enrichment; empty = provider default
		SystemPrompt: "You are a tag extraction assistant. Return only comma-separated lowercase tags.",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock(prompt)},
		},
		MaxTokens: 50,
	}

	resp, err := e.provider.Chat(jobCtx, req)
	if err != nil {
		slog.Warn("enricher: LLM call failed", "entry_id", job.entry.ID, "error", err)
		return
	}

	tags := parseEnrichTags(resp.Content)
	if len(tags) == 0 {
		slog.Debug("enricher: empty tag response, skipping update", "entry_id", job.entry.ID)
		return
	}

	updated := job.entry
	updated.Tags = tags

	updateCtx, updateCancel := context.WithTimeout(ctx, 5*time.Second)
	defer updateCancel()

	if err := e.store.UpdateMemory(updateCtx, job.entry.ScopeID, updated); err != nil {
		slog.Warn("enricher: UpdateMemory failed", "entry_id", job.entry.ID, "error", err)
	} else {
		slog.Debug("enricher: tags updated", "entry_id", job.entry.ID, "tags", tags)
	}
}

// Stop signals the worker goroutine to exit and waits for it to finish.
// Safe to call multiple times — subsequent calls are no-ops.
func (e *Enricher) Stop() {
	e.stopOnce.Do(func() { close(e.ch) })
	e.wg.Wait()
}

// parseEnrichTags splits a comma-separated tag string, trimming whitespace and
// filtering empty entries.
func parseEnrichTags(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// resolveEnrichModel returns the override when set, otherwise an empty string
// which signals to callers to use the provider's configured default model
// (the same one the main agent uses).
//
// Rationale: previously this function returned a "cheapest per provider"
// hardcoded model (e.g. meta-llama/llama-3-8b-instruct:free on OpenRouter),
// but those model names go stale — OpenRouter retires free-tier models
// without notice, causing 404s. Respecting the user's already-chosen model
// is safer. Users who want a cheaper classifier can set agent.enrich_model.
func resolveEnrichModel(_ provider.Provider, override string) string {
	return override
}
