package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"microagent/internal/config"
	"microagent/internal/provider"
	"microagent/internal/store"
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
// Non-blocking: if the channel is full the job is silently dropped.
func (e *Enricher) Enqueue(entry store.MemoryEntry) {
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
			{Role: "user", Content: prompt},
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
func (e *Enricher) Stop() {
	close(e.ch)
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

// resolveEnrichModel returns the cheapest model for the given provider.
// If override is set, it takes precedence.
func resolveEnrichModel(prov provider.Provider, override string) string {
	if override != "" {
		return override
	}
	switch prov.Name() {
	case "anthropic":
		return "claude-haiku-3-5"
	case "gemini":
		return "gemini-2.0-flash-lite"
	case "openai":
		return "gpt-4o-mini"
	case "openrouter":
		return "meta-llama/llama-3-8b-instruct:free"
	default:
		return "" // use provider's configured default
	}
}
