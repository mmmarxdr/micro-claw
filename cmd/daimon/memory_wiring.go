package main

import (
	"log/slog"

	"daimon/internal/agent"
	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/store"
	"daimon/internal/tool"
)

// wireSmartMemory attaches the Curator, Consolidator, and memory tools to ag.
// It reads from cfg.Agent.Memory and registers memory tools in toolsRegistry.
// This function is idempotent with respect to existing tool registrations —
// it skips any tool name that already exists in the registry.
func wireSmartMemory(
	ag *agent.Agent,
	prov provider.Provider,
	st store.Store,
	cfg *config.Config,
	toolsRegistry map[string]tool.Tool,
) {
	memCfg := cfg.Agent.Memory

	// Wire Curator.
	if memCfg.Curation.Enabled {
		curator := agent.NewCurator(
			prov, st,
			ag.Enricher(), ag.EmbeddingWorker(),
			memCfg.Curation, memCfg.Deduplication,
		)
		if curator != nil {
			ag.WithCurator(curator)
			slog.Info("memory curation enabled",
				"min_importance", memCfg.Curation.MinImportance,
				"min_chars", memCfg.Curation.MinResponseChars,
			)
		}
	}

	// Wire Consolidator.
	if memCfg.Consolidation.Enabled {
		consolidator := agent.NewConsolidator(
			prov, st,
			ag.Enricher(), ag.EmbeddingWorker(),
			memCfg.Consolidation,
		)
		if consolidator != nil {
			ag.WithConsolidator(consolidator)
			slog.Info("memory consolidation enabled",
				"interval_hours", memCfg.Consolidation.IntervalHours,
			)
		}
	}

	// Register memory tools.
	deps := tool.MemoryToolDeps{Store: st}
	if ag.Enricher() != nil {
		enricher := ag.Enricher()
		deps.EnqueueEnrich = func(entry store.MemoryEntry) { enricher.Enqueue(entry) }
	}
	if ag.EmbeddingWorker() != nil {
		embWorker := ag.EmbeddingWorker()
		deps.EnqueueEmbed = func(id, scope, content string) { embWorker.Enqueue(id, scope, content) }
	}
	memTools := tool.BuildMemoryTools(deps)
	for name, t := range memTools {
		if _, exists := toolsRegistry[name]; !exists {
			toolsRegistry[name] = t
			slog.Debug("memory tool registered", "tool", name)
		}
	}
}
