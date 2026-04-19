package main

import (
	"context"
	"log/slog"

	"daimon/internal/agent"
	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/store"
	"daimon/internal/tool"
)

// wireRAG sets up the RAG subsystem: DocumentStore, DocIngestionWorker, and RAG tools.
// Returns the worker (caller must call Start and Stop), or nil when RAG is disabled.
//
// Wiring contract:
//   - RAG requires SQLite store — silently skips for other backends.
//   - embedFn is derived from the provider when it implements provider.EmbeddingProvider.
//   - Tools are registered in toolsRegistry (won't overwrite existing entries).
func wireRAG(
	cfg *config.Config,
	st store.Store,
	prov provider.Provider,
	ag *agent.Agent,
	toolsRegistry map[string]tool.Tool,
) *rag.DocIngestionWorker {
	if !cfg.RAG.Enabled {
		return nil
	}

	sqlStore, ok := st.(*store.SQLiteStore)
	if !ok {
		slog.Warn("rag: store does not implement *SQLiteStore; RAG disabled")
		return nil
	}

	db := sqlStore.DB()
	ragCfg := cfg.RAG
	docStore := rag.NewSQLiteDocumentStore(db, ragCfg.MaxDocuments, ragCfg.MaxChunks)

	// Derive embed function from provider when available.
	var embedFn func(ctx context.Context, text string) ([]float32, error)
	if ep, ok := prov.(provider.EmbeddingProvider); ok {
		embedFn = func(ctx context.Context, text string) ([]float32, error) {
			return ep.Embed(ctx, text)
		}
	}

	// Wire document store into the agent for per-turn RAG search.
	ag.WithRAGStore(docStore, embedFn, ragCfg.TopK, ragCfg.MaxContextTokens)

	// Build the ingestion worker.
	extractor := rag.NewSelectExtractor(rag.MarkdownExtractor{}, rag.PlainTextExtractor{})
	chunker := rag.FixedSizeChunker{}

	var mediaReader rag.MediaStoreReader
	if ms, ok := st.(store.MediaStore); ok {
		mediaReader = ms
	}

	worker := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:      docStore,
		Extractor:  extractor,
		Chunker:    chunker,
		EmbedFn:    embedFn,
		MediaStore: mediaReader,
		ChunkOpts: rag.ChunkOptions{
			Size:    ragCfg.ChunkSize,
			Overlap: ragCfg.ChunkOverlap,
		},
	})

	// Register RAG tools (index_doc, search_docs).
	ragTools := rag.BuildRAGTools(rag.RAGToolDeps{
		Worker:  worker,
		Store:   docStore,
		EmbedFn: embedFn,
	})
	for _, t := range ragTools {
		name := t.Name()
		if _, exists := toolsRegistry[name]; !exists {
			toolsRegistry[name] = t
			slog.Info("rag: registered tool", "tool", name)
		}
	}

	slog.Info("rag: subsystem enabled",
		"chunk_size", ragCfg.ChunkSize,
		"top_k", ragCfg.TopK,
		"max_context_tokens", ragCfg.MaxContextTokens,
	)

	return worker
}
