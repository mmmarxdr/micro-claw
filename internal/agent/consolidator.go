package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// Consolidator is a background worker that periodically groups memory entries
// by Topic and merges older entries into a single consolidated summary via an
// LLM call. It runs on a configurable ticker interval and archives the original
// entries after merging.
//
// NewConsolidator returns nil when consolidation is disabled — callers must nil-check.
type Consolidator struct {
	prov      provider.Provider
	store     store.Store
	enricher  *Enricher        // may be nil
	embWorker *EmbeddingWorker // may be nil
	model     string           // cheap model for consolidation prompts
	cfg       config.ConsolidationConfig
	wg        sync.WaitGroup
	cancel    context.CancelFunc
}

// NewConsolidator constructs a Consolidator.
// Returns nil if cfg.Enabled is false.
func NewConsolidator(
	prov provider.Provider,
	st store.Store,
	enricher *Enricher,
	embWorker *EmbeddingWorker,
	cfg config.ConsolidationConfig,
) *Consolidator {
	if !cfg.Enabled {
		return nil
	}

	// Apply defaults.
	if cfg.IntervalHours <= 0 {
		cfg.IntervalHours = 24
	}
	if cfg.MinEntriesPerTopic <= 0 {
		cfg.MinEntriesPerTopic = 5
	}
	if cfg.KeepNewest <= 0 {
		cfg.KeepNewest = 3
	}

	return &Consolidator{
		prov:      prov,
		store:     st,
		enricher:  enricher,
		embWorker: embWorker,
		model:     resolveEnrichModel(prov, ""),
		cfg:       cfg,
	}
}

// Start launches the background consolidation goroutine.
// The goroutine exits when the provided ctx is cancelled or Stop is called.
func (c *Consolidator) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	c.wg.Add(1)
	go c.run(runCtx)
}

// Stop signals the consolidation goroutine to exit and waits for it to finish.
// Safe to call even if Start was never called.
func (c *Consolidator) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// run is the internal ticker loop.
func (c *Consolidator) run(ctx context.Context) {
	defer c.wg.Done()

	interval := time.Duration(c.cfg.IntervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.consolidateAll(ctx); err != nil {
				slog.Warn("consolidator: consolidateAll failed", "error", err)
			}
		}
	}
}

// consolidateAll iterates all memory scopes and consolidates each one.
func (c *Consolidator) consolidateAll(ctx context.Context) error {
	// Require SQLiteStore for scope listing.
	sqlSt, ok := c.store.(*store.SQLiteStore)
	if !ok {
		return nil
	}

	scopes, err := sqlSt.ListMemoryScopes(ctx)
	if err != nil {
		return fmt.Errorf("listing scopes: %w", err)
	}

	totalArchived := 0
	totalCreated := 0
	for _, scope := range scopes {
		archived, created, err := c.consolidateScope(ctx, scope)
		if err != nil {
			slog.Warn("consolidator: scope failed", "scope", scope, "error", err)
			continue
		}
		totalArchived += archived
		totalCreated += created
	}

	if totalArchived > 0 || totalCreated > 0 {
		slog.Info("consolidator: run complete",
			"scopes", len(scopes),
			"archived", totalArchived,
			"created", totalCreated)
	}
	return nil
}

// consolidateScope consolidates memories in a single scope.
// Returns counts of archived and created entries.
func (c *Consolidator) consolidateScope(ctx context.Context, scope string) (archived, created int, err error) {
	// Fetch all non-archived entries for this scope (limit=0 → no limit).
	entries, err := c.store.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		return 0, 0, fmt.Errorf("searching memories for scope %s: %w", scope, err)
	}

	// Group by Topic, skipping empty topics.
	topicGroups := make(map[string][]store.MemoryEntry)
	for _, e := range entries {
		if e.Topic == "" {
			continue
		}
		topicGroups[e.Topic] = append(topicGroups[e.Topic], e)
	}

	for topic, group := range topicGroups {
		if len(group) <= c.cfg.MinEntriesPerTopic {
			continue
		}

		a, cr, mergeErr := c.consolidateTopic(ctx, scope, topic, group)
		if mergeErr != nil {
			slog.Warn("consolidator: topic merge failed, skipping",
				"scope", scope, "topic", topic, "error", mergeErr)
			continue
		}
		archived += a
		created += cr
	}

	if archived > 0 || created > 0 {
		slog.Debug("consolidator: scope done",
			"scope", scope, "archived", archived, "created", created)
	}
	return archived, created, nil
}

// consolidateTopic merges the oldest entries in a topic group into a single LLM summary.
// It keeps the newest cfg.KeepNewest entries untouched and archives the rest.
func (c *Consolidator) consolidateTopic(ctx context.Context, scope, topic string, group []store.MemoryEntry) (archived, created int, err error) {
	// Sort ascending (oldest first).
	sort.Slice(group, func(i, j int) bool {
		return group[i].CreatedAt.Before(group[j].CreatedAt)
	})

	keepCount := c.cfg.KeepNewest
	if keepCount >= len(group) {
		return 0, 0, nil // nothing to consolidate
	}

	candidates := group[:len(group)-keepCount]

	// Build consolidation prompt.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Consolidate these memories about \"%s\" into a single comprehensive summary. Preserve all facts, preferences, and decisions.\n\n", topic))
	for i, e := range candidates {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, e.Content))
	}

	llmCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req := provider.ChatRequest{
		Model:        c.model,
		SystemPrompt: "You are a memory consolidation assistant. Return only the consolidated summary text without any preamble.",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock(sb.String())},
		},
		MaxTokens: 500,
	}

	resp, err := c.prov.Chat(llmCtx, req)
	if err != nil {
		return 0, 0, fmt.Errorf("LLM consolidation failed: %w", err)
	}

	consolidated := strings.TrimSpace(resp.Content)
	if consolidated == "" {
		return 0, 0, fmt.Errorf("LLM returned empty consolidation")
	}

	// Compute max importance from candidates.
	maxImportance := 0
	for _, e := range candidates {
		if e.Importance > maxImportance {
			maxImportance = e.Importance
		}
	}
	if maxImportance < 7 {
		maxImportance = 7 // consolidated entries are at least 7 (they summarize history)
	}

	// Derive type from the first candidate.
	entryType := ""
	if len(candidates) > 0 {
		entryType = candidates[0].Type
	}

	// Create consolidated entry.
	newEntry := store.MemoryEntry{
		ID:         uuid.New().String(),
		ScopeID:    scope,
		Topic:      topic,
		Type:       entryType,
		Title:      fmt.Sprintf("[consolidated] %s", topic),
		Content:    consolidated,
		Source:     "consolidator",
		Importance: maxImportance,
		CreatedAt:  time.Now(),
	}

	if err := c.store.AppendMemory(ctx, scope, newEntry); err != nil {
		return 0, 0, fmt.Errorf("appending consolidated entry: %w", err)
	}
	created++

	// Enqueue async workers for new entry.
	if c.enricher != nil {
		c.enricher.Enqueue(newEntry)
	}
	if c.embWorker != nil {
		c.embWorker.Enqueue(newEntry.ID, scope, consolidated)
	}

	// Archive original candidates.
	now := time.Now()
	for _, e := range candidates {
		e.ArchivedAt = &now
		if archiveErr := c.store.UpdateMemory(ctx, scope, e); archiveErr != nil {
			slog.Warn("consolidator: failed to archive entry",
				"entry_id", e.ID, "error", archiveErr)
			// Continue — don't fail the whole operation for a single archive failure.
		} else {
			archived++
		}
	}

	return archived, created, nil
}
