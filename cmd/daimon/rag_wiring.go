package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"daimon/internal/agent"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
	"daimon/internal/store"
	"daimon/internal/tool"
)

// Compile-time assertions: config.*Conf and rag.*Conf must stay in sync
// (dual-mirror pattern per proposal §2.9 risk table). If a field is added
// to one and not the other, this assignment will fail at compile time.
var _ = config.RAGHydeConf(rag.RAGHydeConf{})
var _ = config.RAGMetricsConf(rag.RAGMetricsConf{})

// RAGWiring is what wireRAG returns when RAG is enabled: the background
// ingestion worker (caller owns lifecycle via Start/Stop), the DocumentStore
// (used by web handlers for list/delete/get), the embed function if the
// active provider supports embeddings, and the metrics recorder for the
// GET /api/metrics/rag endpoint. All fields are nil when RAG is disabled.
type RAGWiring struct {
	Worker   *rag.DocIngestionWorker
	Store    rag.DocumentStore
	EmbedFn  func(ctx context.Context, text string) ([]float32, error)
	MediaRef rag.MediaStoreReader
	Metrics  *metrics.RingRecorder
}

// wireRAG sets up the RAG subsystem: DocumentStore, DocIngestionWorker, and RAG tools.
// Returns a RAGWiring with nil fields when RAG is disabled.
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
) RAGWiring {
	if !cfg.RAG.Enabled {
		return RAGWiring{}
	}

	sqlStore, ok := st.(*store.SQLiteStore)
	if !ok {
		slog.Warn("rag: store does not implement *SQLiteStore; RAG disabled")
		return RAGWiring{}
	}

	db := sqlStore.DB()
	ragCfg := cfg.RAG
	docStore := rag.NewSQLiteDocumentStore(db, ragCfg.MaxDocuments, ragCfg.MaxChunks)

	// Remove trailing junk chunks left by the pre-fix chunker bug.
	// Idempotent and safe — log counts, never fail startup on error.
	if scanned, deleted, cleanErr := docStore.CleanupJunkChunks(context.Background()); cleanErr != nil {
		slog.Warn("rag: cleanup junk chunks failed", "error", cleanErr)
	} else {
		slog.Info("rag: cleanup junk chunks", "docs_scanned", scanned, "chunks_deleted", deleted)
	}

	// Derive the embed function. When rag.embedding.enabled, build a SEPARATE
	// provider just for embeddings (lets users pair OpenRouter for chat with
	// OpenAI/Gemini for vectors). Falls back to the main chat provider when
	// the user hasn't opted in — backwards-compatible with the prior wiring.
	embedFn := buildEmbedFn(ragCfg.Embedding, prov)

	// Wire document store into the agent for per-turn RAG search.
	ag.WithRAGStore(docStore, embedFn, ragCfg.TopK, ragCfg.MaxContextTokens)

	// Propagate retrieval precision config (neighbor radius, BM25/cosine
	// thresholds) into the agent. Without this call, SearchOptions construction
	// in the loop + search_docs tool silently falls back to zero-valued defaults
	// regardless of what the user set in rag.retrieval.*. This was a real
	// regression — shipped dead for ~24h before an audit caught it.
	ag.WithRAGRetrievalConf(rag.RAGRetrievalConf{
		NeighborRadius: ragCfg.Retrieval.NeighborRadius,
		MaxBM25Score:   ragCfg.Retrieval.MaxBM25Score,
		MinCosineScore: ragCfg.Retrieval.MinCosineScore,
	})

	// Construct the in-memory metrics ring buffer (always-on when RAG is enabled).
	// Default capacity: N=200. The recorder is passed to the agent and the web
	// server so both the retrieval path and the API endpoint share the same ring.
	metricsRec := metrics.NewRingRecorder(200)
	ag.WithRAGMetrics(metricsRec)

	// Build the ingestion worker. PDF and DOCX extractors ordered first so
	// structured formats resolve before the generic text/markdown fallback.
	// PdftotextExtractor goes BEFORE PdfExtractor — when poppler-utils is
	// installed it handles LaTeX/CID-encoded PDFs the pure-Go lib can't, and
	// when absent its Supports() returns false so the chain falls through
	// transparently.
	extractor := rag.NewSelectExtractor(
		rag.PdftotextExtractor{},
		rag.PdfExtractor{},
		rag.DocxExtractor{},
		rag.MarkdownExtractor{},
		rag.PlainTextExtractor{},
	)
	chunker := rag.FixedSizeChunker{}

	var mediaReader rag.MediaStoreReader
	if ms, ok := st.(store.MediaStore); ok {
		mediaReader = ms
	}

	// Build the summary function — 1-shot Haiku-class call invoked post-extract,
	// pre-persist. Nil when no provider is wired (tests / disabled RAG). The
	// optional `rag.summary_model` override lets users point this at a cheaper
	// model than the main chat without touching agent.enrich_model.
	summaryFn := buildSummaryFn(prov, ragCfg.SummaryModel)

	// Build the hypothesis function for HyDE. Model fallback chain resolved at
	// wire time: hyde.model → rag.summary_model → "" (provider's default model).
	// Only constructed when HyDE is enabled to avoid unnecessary provider calls.
	if ragCfg.Hyde.Enabled {
		hydeModel := ragCfg.Hyde.Model
		if hydeModel == "" {
			hydeModel = ragCfg.SummaryModel
		}
		hypothesisFn := buildHypothesisFn(prov, hydeModel)
		slog.Info("rag: HyDE enabled", "model", hydeModel)
		hydeCfg := ragCfg.Hyde
		hydeCfg.Model = hydeModel // persist resolved model for logging
		ag.WithRAGHydeConf(hydeCfg, hypothesisFn)
	}

	// Pace single-call embeds when the active provider has a known low quota.
	// Gemini free tier caps at 100 req/min/model — 700ms keeps us under at
	// ~85 req/min. Batched embeds bypass this throttle (one HTTP call per N
	// chunks), so this only matters when batching isn't available.
	var embedThrottle time.Duration
	if ragCfg.Embedding.Enabled && ragCfg.Embedding.Provider == "gemini" {
		embedThrottle = 700 * time.Millisecond
	}

	// Bind the batch path when the embedding provider implements it. Built
	// alongside the single-call closure so the worker can prefer batch and
	// fall back to single-call cleanly when the type assertion fails.
	embedBatchFn := buildEmbedBatchFn(ragCfg.Embedding, prov)

	worker := rag.NewDocIngestionWorker(rag.DocIngestionWorkerConfig{
		Store:         docStore,
		Extractor:     extractor,
		Chunker:       chunker,
		EmbedFn:       embedFn,
		EmbedBatchFn:  embedBatchFn,
		SummaryFn:     summaryFn,
		MediaStore:    mediaReader,
		EmbedThrottle: embedThrottle,
		ChunkOpts: rag.ChunkOptions{
			Size:    ragCfg.ChunkSize,
			Overlap: ragCfg.ChunkOverlap,
		},
	})

	// Build the hypothesis function for the tool path. Mirrors the agent-loop
	// wiring above: only constructed when HyDE is enabled. The tool path shares
	// the same HyDE config and model resolution so both paths stay in sync.
	var toolHypothesisFn func(ctx context.Context, query string) (string, error)
	if ragCfg.Hyde.Enabled {
		hydeModel := ragCfg.Hyde.Model
		if hydeModel == "" {
			hydeModel = ragCfg.SummaryModel
		}
		toolHypothesisFn = buildHypothesisFn(prov, hydeModel)
	}

	// Register RAG tools (index_doc, search_docs).
	// search_docs now runs HyDE when enabled — same config as the agent loop.
	ragTools := rag.BuildRAGTools(rag.RAGToolDeps{
		Worker:  worker,
		Store:   docStore,
		EmbedFn: embedFn,
		HypothesisFn: toolHypothesisFn,
		HydeConf: rag.HydeSearchConfig{
			Enabled:           ragCfg.Hyde.Enabled,
			HypothesisTimeout: ragCfg.Hyde.HypothesisTimeout,
			QueryWeight:       ragCfg.Hyde.QueryWeight,
			MaxCandidates:     ragCfg.Hyde.MaxCandidates,
		},
		RetrievalConf: rag.RetrievalSearchConfig{
			NeighborRadius: ragCfg.Retrieval.NeighborRadius,
			MaxBM25Score:   ragCfg.Retrieval.MaxBM25Score,
			MinCosineScore: ragCfg.Retrieval.MinCosineScore,
		},
		Recorder: metricsRec,
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

	return RAGWiring{
		Worker:   worker,
		Store:    docStore,
		EmbedFn:  embedFn,
		MediaRef: mediaReader,
		Metrics:  metricsRec,
	}
}

// buildEmbedFn returns the function the worker calls to embed each chunk, plus
// the function the agent uses for query embedding at search time. The
// returned closure is nil when no embedding source is available — callers
// should treat nil as "no embeddings" (RAG falls back to FTS5 keyword search).
//
// Resolution order:
//  1. embedCfg.Enabled  → build a dedicated provider from embedCfg and use it.
//  2. main provider implements EmbeddingProvider → reuse it.
//  3. otherwise → nil (no embeddings; RAG still works via FTS5).
//
// A construction failure for the dedicated provider falls back to (2) so a
// misconfigured embedding block doesn't take down RAG entirely.
func buildEmbedFn(embedCfg config.RAGEmbeddingConf, mainProv provider.Provider) func(ctx context.Context, text string) ([]float32, error) {
	if embedCfg.Enabled {
		ep, err := newEmbeddingProvider(embedCfg)
		if err != nil {
			slog.Warn("rag: failed to build dedicated embedding provider, falling back to main provider", "error", err)
		} else if ep != nil {
			slog.Info("rag: using dedicated embedding provider", "provider", embedCfg.Provider, "model", embedCfg.Model)
			return func(ctx context.Context, text string) ([]float32, error) {
				return ep.Embed(ctx, text)
			}
		}
	}
	if ep, ok := mainProv.(provider.EmbeddingProvider); ok {
		return func(ctx context.Context, text string) ([]float32, error) {
			return ep.Embed(ctx, text)
		}
	}
	return nil
}

// buildEmbedBatchFn returns the batched embed closure when the active
// embedding provider implements BatchEmbeddingProvider. Returns nil when no
// batch source is available — the worker falls back to single-call EmbedFn.
//
// Resolution order mirrors buildEmbedFn:
//  1. embedCfg.Enabled  → build a dedicated provider, type-assert.
//  2. main provider implements BatchEmbeddingProvider → reuse it.
//  3. otherwise → nil (worker uses single-call path).
func buildEmbedBatchFn(embedCfg config.RAGEmbeddingConf, mainProv provider.Provider) func(ctx context.Context, texts []string) ([][]float32, error) {
	if embedCfg.Enabled {
		ep, err := newEmbeddingProvider(embedCfg)
		if err == nil && ep != nil {
			if bp, ok := ep.(provider.BatchEmbeddingProvider); ok {
				slog.Info("rag: dedicated embedding provider supports batching", "provider", embedCfg.Provider)
				return func(ctx context.Context, texts []string) ([][]float32, error) {
					return bp.EmbedBatch(ctx, texts)
				}
			}
		}
	}
	if bp, ok := mainProv.(provider.BatchEmbeddingProvider); ok {
		return func(ctx context.Context, texts []string) ([][]float32, error) {
			return bp.EmbedBatch(ctx, texts)
		}
	}
	return nil
}

// newEmbeddingProvider constructs the OpenAI or Gemini provider used solely
// for embeddings. The chat-side `Model` field is intentionally left empty
// because these instances never serve completions — only Embed().
func newEmbeddingProvider(c config.RAGEmbeddingConf) (provider.EmbeddingProvider, error) {
	provCfg := config.ProviderConfig{
		Type:    c.Provider,
		APIKey:  c.APIKey,
		BaseURL: c.BaseURL,
	}
	switch c.Provider {
	case "openai":
		p, err := provider.NewOpenAIProvider(provCfg)
		if err != nil {
			return nil, err
		}
		return p.WithEmbeddingModel(c.Model), nil
	case "gemini":
		p := provider.NewGeminiProvider(provCfg)
		return p.WithEmbeddingModel(c.Model), nil
	default:
		return nil, fmt.Errorf("rag: embedding provider %q not supported", c.Provider)
	}
}

// buildHypothesisFn returns a closure that generates a hypothetical document
// excerpt for the given query (the HyDE technique). Mirrors buildSummaryFn:
// modelOverride resolves as hyde.model → rag.summary_model → "" (provider
// default). Returns nil when prov is nil — callers treat nil as "HyDE disabled".
//
// Prompt text is frozen per proposal §2.7 — do not rewrite.
func buildHypothesisFn(prov provider.Provider, modelOverride string) func(ctx context.Context, query string) (string, error) {
	if prov == nil {
		return nil
	}
	return func(ctx context.Context, query string) (string, error) {
		prompt := "Write a realistic excerpt from a document that would best answer the user's query.\n" +
			"Write 2-4 sentences, plain prose, no preamble or framing, as if extracted verbatim.\n" +
			"If the query asks to find or summarize a document, describe what that document contains.\n\n" +
			"Query: " + query

		req := provider.ChatRequest{
			Model:        modelOverride,
			SystemPrompt: "You are a retrieval assistant. Respond with a realistic document excerpt only.",
			Messages: []provider.ChatMessage{
				{Role: "user", Content: content.TextBlock(prompt)},
			},
			MaxTokens: 200,
		}
		resp, err := prov.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(resp.Content), nil
	}
}

// buildSummaryFn returns a closure that asks the active provider for a concise
// 1-2 sentence summary of an ingested document. The closure is safe to pass to
// DocIngestionWorkerConfig.SummaryFn. Returns nil when prov is nil — the
// worker treats nil as "no summary generation" and leaves Document.Summary
// empty.
//
// modelOverride lets the operator pin summarization to a cheap model
// (e.g. Haiku) without affecting the main chat model. Empty string means the
// provider chooses its configured default.
//
// Prompt shape mirrors the Curator's editorial voice: no preamble, no
// "here is a summary" framing, just the sentence(s). We ask for ≤ 2 sentences
// to keep cost bounded and the Knowledge card body short.
func buildSummaryFn(prov provider.Provider, modelOverride string) func(ctx context.Context, text string) (string, error) {
	if prov == nil {
		return nil
	}
	return func(ctx context.Context, text string) (string, error) {
		prompt := "Summarize this document in 1-2 sentences. " +
			"Describe its purpose and main content; do not include preambles or framing. " +
			"Plain prose, no bullets, no markdown.\n\n" + text

		req := provider.ChatRequest{
			Model:        modelOverride,
			SystemPrompt: "You are a documentation summarizer. Respond with 1-2 concise sentences only.",
			Messages: []provider.ChatMessage{
				{Role: "user", Content: content.TextBlock(prompt)},
			},
			MaxTokens: 120,
		}
		resp, err := prov.Chat(ctx, req)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(resp.Content), nil
	}
}
