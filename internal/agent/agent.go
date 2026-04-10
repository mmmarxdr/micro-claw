package agent

import (
	"context"
	"log/slog"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/provider"
	"microagent/internal/skill"
	"microagent/internal/store"
	"microagent/internal/tool"
)

// startPruningLoop launches the memory pruning goroutine. It is a no-op when
// the store is not a *SQLiteStore (pruning is SQLite-only).
//
// One goroutine runs PruneMemories once at startup and a second goroutine
// fires on cfg.PruneInterval. Both exit cleanly when ctx is cancelled.
func (a *Agent) startPruningLoop(ctx context.Context) {
	sqlStore, ok := a.store.(*store.SQLiteStore)
	if !ok {
		return
	}
	cfg := store.PruneConfig{
		Threshold:     a.config.PruneThreshold,
		RetentionDays: a.config.PruneRetentionDays,
		Lambda:        0.03,
		BoostFactor:   0.5,
	}

	// Startup prune — runs once in its own goroutine so it doesn't block Run.
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p, d, err := sqlStore.PruneMemories(ctx, cfg)
		if err != nil {
			slog.Warn("startup pruning failed", "error", err)
		} else {
			slog.Info("startup pruning complete", "soft_deleted", p, "hard_deleted", d)
		}
	}()

	// Periodic ticker goroutine.
	interval := a.config.PruneInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p, d, err := sqlStore.PruneMemories(ctx, cfg)
				if err != nil {
					slog.Warn("periodic pruning failed", "error", err)
				} else if p > 0 || d > 0 {
					slog.Info("periodic pruning complete", "soft_deleted", p, "hard_deleted", d)
				}
			}
		}
	}()
}

type Agent struct {
	config          config.AgentConfig
	limits          config.LimitsConfig
	filterCfg       config.FilterConfig
	ctxModeCfg      config.ContextModeConfig // context-mode configuration
	channel         channel.Channel
	provider        provider.Provider
	store           store.Store
	outputStore     store.OutputStore // for auto-indexing tool outputs
	auditor         audit.Auditor
	tools           map[string]tool.Tool
	skills          []skill.SkillContent
	skillIndex      skill.SkillIndex
	sem             chan struct{}    // concurrency semaphore
	stream          bool             // true when streaming is enabled and provider supports it
	enricher        *Enricher        // async tag enrichment worker; nil when disabled
	embeddingWorker *EmbeddingWorker // async embedding worker; nil when disabled
}

func New(
	cfg config.AgentConfig,
	limits config.LimitsConfig,
	filterCfg config.FilterConfig,
	ch channel.Channel,
	prov provider.Provider,
	st store.Store,
	auditor audit.Auditor,
	tools map[string]tool.Tool,
	skills []skill.SkillContent,
	skillIndex skill.SkillIndex,
	maxConcurrent int,
	stream bool,
	storeCfg ...config.StoreConfig,
) *Agent {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	// Only enable streaming if the provider actually implements StreamingProvider.
	enableStream := stream
	if enableStream {
		if _, ok := prov.(provider.StreamingProvider); !ok {
			slog.Warn("streaming enabled in config but provider does not implement StreamingProvider, falling back to sync")
			enableStream = false
		}
	}

	// Wire embedding worker if store is SQLite and embeddings are enabled.
	var embWorker *EmbeddingWorker
	var sCfg config.StoreConfig
	if len(storeCfg) > 0 {
		sCfg = storeCfg[0]
	}
	if sqlStore, ok := st.(*store.SQLiteStore); ok && sCfg.Embeddings {
		embWorker = NewEmbeddingWorker(prov, sqlStore.DB(), sCfg)
		if embWorker != nil {
			// Capture the type assertion outside the closure — safe because
			// NewEmbeddingWorker already verified prov implements EmbeddingProvider.
			ep := prov.(provider.EmbeddingProvider)
			// Register the embed function for two-phase search reranking.
			sqlStore.SetEmbedQueryFunc(func(ctx context.Context, text string) ([]float32, error) {
				return ep.Embed(ctx, text)
			})
		}
	}

	// Extract OutputStore if available (for auto-indexing)
	var outputStore store.OutputStore
	if sqlStore, ok := st.(store.OutputStore); ok {
		outputStore = sqlStore
	}

	return &Agent{
		config:          cfg,
		limits:          limits,
		filterCfg:       filterCfg,
		ctxModeCfg:      cfg.ContextMode,
		channel:         ch,
		provider:        prov,
		store:           st,
		outputStore:     outputStore,
		auditor:         auditor,
		tools:           tools,
		skills:          skills,
		skillIndex:      skillIndex,
		sem:             make(chan struct{}, maxConcurrent),
		stream:          enableStream,
		enricher:        NewEnricher(prov, st, cfg),
		embeddingWorker: embWorker,
	}
}

func (a *Agent) Run(ctx context.Context) error {
	inbox := make(chan channel.IncomingMessage, 100)

	if err := a.channel.Start(ctx, inbox); err != nil {
		return err
	}

	// Start background workers.
	if a.enricher != nil {
		a.enricher.Start(ctx)
	}
	if a.embeddingWorker != nil {
		a.embeddingWorker.Start(ctx)
	}
	a.startPruningLoop(ctx)

	slog.Info("agent loop started")

	for {
		select {
		case <-ctx.Done():
			// Drain semaphore — wait for in-flight messages to complete.
			for i := 0; i < cap(a.sem); i++ {
				a.sem <- struct{}{}
			}
			return ctx.Err()
		case msg := <-inbox:
			go func(m channel.IncomingMessage) {
				select {
				case a.sem <- struct{}{}:
					defer func() { <-a.sem }()
					a.processMessage(ctx, m)
				case <-time.After(30 * time.Second):
					slog.Warn("agent: message dropped, semaphore timeout",
						"channel_id", m.ChannelID,
						"text_preview", truncate(m.Text, 80))
				}
			}(msg)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (a *Agent) Shutdown() error {
	if a.enricher != nil {
		a.enricher.Stop()
	}
	if a.embeddingWorker != nil {
		a.embeddingWorker.Stop()
	}
	return a.channel.Stop()
}
