package agent

import (
	"context"
	"log/slog"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/notify"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
	"daimon/internal/skill"
	"daimon/internal/store"
	"daimon/internal/tool"
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
	mediaCfg        config.MediaConfig // media cleanup configuration
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
	indexWorker     *IndexingWorker  // async output indexing worker; nil when disabled
	curator         *Curator         // smart memory curation; nil when disabled
	consolidator    *Consolidator    // background memory consolidation; nil when disabled
	contextMgr      *ContextManager  // smart context management; nil when disabled
	commands        *CommandRegistry
	startedAt       time.Time
	inbox           chan channel.IncomingMessage
	channelName     string
	bus             notify.Bus

	// RAG fields — nil when RAG is not wired.
	ragStore         rag.DocumentStore
	ragEmbedFn       func(context.Context, string) ([]float32, error)
	ragMaxChunks     int
	ragMaxTokens     int
	ragRetrievalConf rag.RAGRetrievalConf // neighbor expansion + score thresholds

	// HyDE fields — zero/nil when HyDE is disabled.
	ragHydeConf      config.RAGHydeConf
	ragHypothesisFn  func(context.Context, string) (string, error)

	// Metrics recorder — nil means no-op (NoopRecorder equivalent).
	ragMetrics metrics.Recorder
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

	// Wire indexing worker when an OutputStore is present.
	// Agent owns the full lifecycle: created and started here, stopped in Shutdown.
	var idxWorker *IndexingWorker
	if outputStore != nil {
		idxWorker = NewIndexingWorker(outputStore)
		idxWorker.Start(context.Background())
	}

	reg := NewCommandRegistry()
	registerBuiltinCommands(reg)

	// Synthesize ContextConfig from legacy flat fields when cfg.Context is zero-value.
	// Priority:
	//   1. cfg.Context.Strategy is already set → use cfg.Context as-is
	//   2. MaxContextTokens > 0 → strategy "smart"
	//   3. HistoryLength > 0 only → strategy "legacy"
	//   4. Neither → strategy "none"
	ctxCfg := cfg.Context
	if ctxCfg.Strategy == "" {
		switch {
		case cfg.MaxContextTokens > 0:
			ctxCfg.Strategy = "smart"
			ctxCfg.MaxTokens = cfg.MaxContextTokens
			ctxCfg.SummaryMaxTokens = cfg.SummaryTokens
		case cfg.HistoryLength > 0:
			ctxCfg.Strategy = "legacy"
		default:
			ctxCfg.Strategy = "none"
		}
	}
	contextMgr := NewContextManager(ctxCfg, prov, nil)

	// Register compact command as a closure so it can access the agent after construction.
	// legacyFn will be wired after the agent struct is built (needs `a` to call legacyTruncate).
	// This is a two-step registration: we register a placeholder here and fix it up
	// after the agent is built — or we use a post-construction step.
	// Simple approach: register it after the struct is built below.

	a := &Agent{
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
		indexWorker:     idxWorker,
		contextMgr:      contextMgr,
		commands:        reg,
		channelName:     ch.Name(),
	}
	// Wire the legacy truncation function now that the agent struct is fully built.
	// This lets ContextManager.legacyManage delegate to the existing legacyTruncate method.
	// The closure preserves the original guard: only truncate when over HistoryLength.
	if ctxCfg.Strategy == "legacy" {
		histLen := cfg.HistoryLength
		a.contextMgr.legacyFn = func(ctx context.Context, messages []provider.ChatMessage) []provider.ChatMessage {
			if histLen > 0 && len(messages) > histLen {
				return a.legacyTruncate(ctx, messages)
			}
			return messages
		}
	}

	// Register the /compact command now that the agent struct is fully built.
	reg.Register("compact", "Force-compact conversation context", func(cc CommandContext) error {
		return a.cmdCompact(cc)
	})
	return a
}

// WithMediaConfig sets the media configuration on the agent, enabling the
// periodic media cleanup loop in Run(). Call before Run().
func (a *Agent) WithMediaConfig(cfg config.MediaConfig) *Agent {
	a.mediaCfg = cfg
	return a
}

// WithBus sets the event bus on the agent, enabling agent.turn.started/completed events.
// Also propagates the bus to contextMgr if present.
// Returns a for fluent chaining.
func (a *Agent) WithBus(bus notify.Bus) *Agent {
	a.bus = bus
	if a.contextMgr != nil {
		a.contextMgr.bus = bus
	}
	return a
}

// WithCurator sets the Curator on the agent. Call after New(), before Run().
func (a *Agent) WithCurator(c *Curator) { a.curator = c }

// WithConsolidator sets the Consolidator on the agent. Call after New(), before Run().
func (a *Agent) WithConsolidator(c *Consolidator) { a.consolidator = c }

// WithRAGStore wires a DocumentStore into the agent for automatic retrieval-augmented
// generation. On every turn the agent will search for relevant chunks from st and
// inject them into the system prompt.
//
//   - embedFn may be nil (FTS-only search without vector reranking).
//   - maxChunks is the number of top chunks to retrieve per turn (default 5).
//   - maxTokens is the token budget for the RAG section in the prompt (default 10000).
func (a *Agent) WithRAGStore(st rag.DocumentStore, embedFn func(context.Context, string) ([]float32, error), maxChunks, maxTokens int) *Agent {
	a.ragStore = st
	a.ragEmbedFn = embedFn
	if maxChunks <= 0 {
		maxChunks = 5
	}
	if maxTokens <= 0 {
		maxTokens = 10000
	}
	a.ragMaxChunks = maxChunks
	a.ragMaxTokens = maxTokens
	return a
}

// WithRAGRetrievalConf stores retrieval-precision options (neighbor radius,
// score thresholds) that are applied on every SearchChunks call. Call this
// after WithRAGStore when the user has opted into non-default retrieval behavior.
func (a *Agent) WithRAGRetrievalConf(conf rag.RAGRetrievalConf) *Agent {
	a.ragRetrievalConf = conf
	return a
}

// RAGRetrievalConfig returns the retrieval-precision options currently in
// effect on the agent. Exposed so wiring regression tests can verify startup
// paths populated the config (the bug this guards against shipped in PR #2
// and went undetected for ~24h because existing tests only exercised the
// setter directly, never the wiring).
func (a *Agent) RAGRetrievalConfig() rag.RAGRetrievalConf {
	return a.ragRetrievalConf
}

// WithRAGHydeConf stores the HyDE configuration and hypothesis function.
// hypothesisFn may be nil — when nil, HyDE is effectively disabled regardless
// of conf.Enabled, and the baseline retrieval path is always used.
func (a *Agent) WithRAGHydeConf(conf config.RAGHydeConf, hypothesisFn func(context.Context, string) (string, error)) *Agent {
	a.ragHydeConf = conf
	a.ragHypothesisFn = hypothesisFn
	return a
}

// WithRAGMetrics sets the metrics recorder for the RAG retrieval path.
// When r is nil the agent behaves as if a NoopRecorder is set — no panic, no log.
func (a *Agent) WithRAGMetrics(r metrics.Recorder) *Agent {
	a.ragMetrics = r
	return a
}

// Enricher returns the agent's async enrichment worker. May be nil.
func (a *Agent) Enricher() *Enricher { return a.enricher }

// EmbeddingWorker returns the agent's async embedding worker. May be nil.
func (a *Agent) EmbeddingWorker() *EmbeddingWorker { return a.embeddingWorker }

func (a *Agent) Run(ctx context.Context) error {
	a.startedAt = time.Now()
	inbox := make(chan channel.IncomingMessage, 100)
	a.inbox = inbox

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
	if a.consolidator != nil {
		a.consolidator.Start(ctx)
	}
	// indexWorker is started in New() — no need to start here.
	a.startPruningLoop(ctx)

	// Start media cleanup loop when media is enabled and the store supports it.
	if config.BoolVal(a.mediaCfg.Enabled) {
		if _, ok := a.store.(store.MediaStore); ok {
			go a.mediaCleanupLoop(ctx)
		}
	}

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
						"text_preview", truncate(m.Content.TextOnly(), 80))
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
	// Close the bus first — drains pending events while the mux can still deliver.
	if a.bus != nil {
		a.bus.Close()
	}

	// Stop the channel first — no more messages will be enqueued after this.
	err := a.channel.Stop()

	// Drain the index worker after the channel stops feeding new outputs.
	if a.indexWorker != nil {
		a.indexWorker.Stop()
	}

	// Stop remaining background workers.
	if a.consolidator != nil {
		a.consolidator.Stop()
	}
	if a.enricher != nil {
		a.enricher.Stop()
	}
	if a.embeddingWorker != nil {
		a.embeddingWorker.Stop()
	}
	return err
}
