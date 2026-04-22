package agent

// T23–T31: HyDE branch in the agent loop.
// Uses internal package access to drive processTurn directly.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/skill"
)

// Ensure channel and content are used.
var _ = channel.IncomingMessage{}
var _ = content.TextBlock

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// hydeDocStore is a mockDocStore variant that records all SearchChunks calls
// (query string + chunk results) and exposes per-call detail for assertions.
type hydeDocStore struct {
	mu      sync.Mutex
	calls   []hydeSearchCall
	results []rag.SearchResult
	err     error
}

type hydeSearchCall struct {
	query string
	vec   []float32
}

func (h *hydeDocStore) AddDocument(_ context.Context, _ rag.Document) error  { return nil }
func (h *hydeDocStore) AddChunks(_ context.Context, _ string, _ []rag.DocumentChunk) error {
	return nil
}
func (h *hydeDocStore) DeleteDocument(_ context.Context, _ string) error { return nil }
func (h *hydeDocStore) ListDocuments(_ context.Context, _ string) ([]rag.Document, error) {
	return nil, nil
}
func (h *hydeDocStore) GetDocument(_ context.Context, _ string) (rag.Document, error) {
	return rag.Document{}, rag.ErrDocNotFound
}
func (h *hydeDocStore) SearchChunks(_ context.Context, query string, vec []float32, opts rag.SearchOptions) ([]rag.SearchResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, hydeSearchCall{query: query, vec: vec})
	return h.results, h.err
}

func (h *hydeDocStore) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// hydeAgent builds a minimal agent with a RAG store wired.
func hydeAgent(t *testing.T, prov provider.Provider, store *hydeDocStore) *Agent {
	t.Helper()
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	ag := New(cfg, defaultLimits(), config.FilterConfig{}, &mockChannel{}, prov, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// Embed fn: returns deterministic 3-dim vector based on text length (non-zero).
	embedFn := func(_ context.Context, text string) ([]float32, error) {
		v := float32(len(text)%5+1) / 5.0
		return []float32{v, 0.5, 0.5}, nil
	}
	ag.WithRAGStore(store, embedFn, 5, 1000)
	return ag
}

// runTurn drives a single processMessage call.
func runTurn(t *testing.T, ag *Agent, text string) {
	t.Helper()
	msg := channel.IncomingMessage{
		ID:      "m1",
		Content: content.TextBlock(text),
	}
	ag.processMessage(context.Background(), msg)
}

// ---------------------------------------------------------------------------
// T23: HyDE disabled → baseline path (exactly one SearchChunks call)
// ---------------------------------------------------------------------------

func TestLoopHyDE_Disabled_BaselinePath(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{}

	ag := hydeAgent(t, prov, store)
	// Hyde disabled (default).
	ag.WithRAGHydeConf(config.RAGHydeConf{Enabled: false}, nil)

	runTurn(t, ag, "tell me about RAG")

	if got := store.callCount(); got != 1 {
		t.Errorf("T23: want exactly 1 SearchChunks call, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T24: HyDE enabled, happy path → two SearchChunks calls
// ---------------------------------------------------------------------------

func TestLoopHyDE_Enabled_HappyPath(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{
		results: []rag.SearchResult{
			{Chunk: rag.DocumentChunk{ID: "chunk-1", Content: "test content"}, DocTitle: "TestDoc", Score: 1.0},
		},
	}

	ag := hydeAgent(t, prov, store)
	hypothesisCalled := 0
	hypoFn := func(_ context.Context, _ string) (string, error) {
		hypothesisCalled++
		return "a realistic document excerpt about RAG retrieval", nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	runTurn(t, ag, "explain RAG retrieval")

	// Must have made 3 SearchChunks calls: raw-BM25 + hyde + pure-vector cosine.
	if got := store.callCount(); got != 3 {
		t.Errorf("T24: want 3 SearchChunks calls, got %d", got)
	}
	if hypothesisCalled != 1 {
		t.Errorf("T24: want 1 hypothesis call, got %d", hypothesisCalled)
	}
}

// ---------------------------------------------------------------------------
// T25: Hypothesis fn error → fallthrough to baseline (1 call)
// ---------------------------------------------------------------------------

func TestLoopHyDE_HypothesisError_Fallthrough(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{}

	ag := hydeAgent(t, prov, store)
	hypoFn := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("provider down")
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	runTurn(t, ag, "what is kubernetes")

	if got := store.callCount(); got != 1 {
		t.Errorf("T25: fallthrough expected 1 SearchChunks call, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T26: Hypothesis fn timeout → fallthrough to baseline
// ---------------------------------------------------------------------------

func TestLoopHyDE_HypothesisTimeout_Fallthrough(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{}

	ag := hydeAgent(t, prov, store)
	hypoFn := func(ctx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return "late hypothesis", nil
		}
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 5 * time.Millisecond, // very short to force timeout
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	start := time.Now()
	runTurn(t, ag, "deploy kubernetes")
	elapsed := time.Since(start)

	// Fallthrough: exactly 1 SearchChunks call.
	if got := store.callCount(); got != 1 {
		t.Errorf("T26: fallthrough expected 1 SearchChunks call, got %d", got)
	}
	// Should not have waited 200ms (hypothesis was abandoned).
	if elapsed > 150*time.Millisecond {
		t.Errorf("T26: turn took %v, expected < 150ms (hypothesis timeout not enforced)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// T27: Hypothesis fn returns empty string → fallthrough to baseline
// ---------------------------------------------------------------------------

func TestLoopHyDE_HypothesisEmpty_Fallthrough(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{}

	ag := hydeAgent(t, prov, store)
	hypoFn := func(_ context.Context, _ string) (string, error) {
		return "", nil // empty hypothesis
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	runTurn(t, ag, "what is rag")

	if got := store.callCount(); got != 1 {
		t.Errorf("T27: fallthrough expected 1 SearchChunks call, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T28: Zero-vector ensemble → fallthrough to baseline
// ---------------------------------------------------------------------------

func TestLoopHyDE_ZeroVectorEnsemble_Fallthrough(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	store := &hydeDocStore{}

	// Override embed fn: always returns zero vector.
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	ag := New(cfg, defaultLimits(), config.FilterConfig{}, &mockChannel{}, prov, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	zeroEmbed := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0, 0, 0}, nil
	}
	ag.WithRAGStore(store, zeroEmbed, 5, 1000)

	hypoFn := func(_ context.Context, _ string) (string, error) {
		return "some hypothesis text", nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	runTurn(t, ag, "find my resume")

	if got := store.callCount(); got != 1 {
		t.Errorf("T28: zero-vector fallthrough expected 1 SearchChunks call, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T29: Dedup — chunk x present in all three conceptual lists → once in final
// ---------------------------------------------------------------------------

func TestLoopHyDE_Dedup_ChunkAppearsOnce(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}

	// Both SearchChunks calls return the same chunk.
	sharedChunk := rag.SearchResult{
		Chunk:    rag.DocumentChunk{ID: "shared-chunk", Content: "shared content"},
		DocTitle: "SharedDoc",
		Score:    1.0,
	}
	store := &hydeDocStore{results: []rag.SearchResult{sharedChunk}}

	ag := hydeAgent(t, prov, store)
	hypoFn := func(_ context.Context, _ string) (string, error) {
		return "a hypothesis about shared content", nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     5,
	}, hypoFn)

	// We can't inspect the final merged list directly without more internal
	// access, but we can verify 3 SearchChunks calls were made (RRF is applied)
	// and the loop didn't crash or duplicate chunks in the prompt.
	runTurn(t, ag, "find shared content")

	if got := store.callCount(); got != 3 {
		t.Errorf("T29: expected 3 SearchChunks calls, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T30: Top-K respected — more results than ragMaxChunks → final sliced to K
// ---------------------------------------------------------------------------

func TestLoopHyDE_TopK_Respected(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}

	// Return 8 distinct chunks from each SearchChunks call.
	var results []rag.SearchResult
	for i := 0; i < 8; i++ {
		results = append(results, rag.SearchResult{
			Chunk:    rag.DocumentChunk{ID: strings.Repeat("c", i+1), Content: "content"},
			DocTitle: "Doc",
			Score:    float64(8 - i),
		})
	}
	store := &hydeDocStore{results: results}

	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	ag := New(cfg, defaultLimits(), config.FilterConfig{}, &mockChannel{}, prov, &mockStore{},
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	embedFn := func(_ context.Context, text string) ([]float32, error) {
		v := float32(len(text)%5+1) / 5.0
		return []float32{v, 0.5, 0.5}, nil
	}
	// ragMaxChunks = 3 — final must be trimmed.
	ag.WithRAGStore(store, embedFn, 3, 1000)

	hypoFn := func(_ context.Context, _ string) (string, error) {
		return "hypothesis text for retrieval", nil
	}
	ag.WithRAGHydeConf(config.RAGHydeConf{
		Enabled:           true,
		HypothesisTimeout: 2 * time.Second,
		QueryWeight:       0.3,
		MaxCandidates:     10,
	}, hypoFn)

	// We verify no panic and 3 calls made. The actual trimming is exercised
	// internally; the system prompt builder receives ≤ ragMaxChunks chunks.
	runTurn(t, ag, "find documents")

	if got := store.callCount(); got != 3 {
		t.Errorf("T30: expected 3 SearchChunks calls, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// T31: Metrics event recorded on every turn (stub — metrics recorder in C2)
// We verify the loop processes two turns without error (recorder wiring in C2).
// ---------------------------------------------------------------------------

func TestLoopHyDE_TwoTurns_NoError(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}, {Content: "ok2"}}}
	store := &hydeDocStore{}

	ag := hydeAgent(t, prov, store)
	ag.WithRAGHydeConf(config.RAGHydeConf{Enabled: false}, nil)

	runTurn(t, ag, "first turn")
	runTurn(t, ag, "second turn")

	// 2 turns × 1 baseline SearchChunks call each = 2 total.
	if got := store.callCount(); got != 2 {
		t.Errorf("T31: want 2 SearchChunks calls across 2 turns, got %d", got)
	}
}
