package agent

// T6.2 tests: WithRAGStore option and RAG search hook in the message loop.

import (
	"context"
	"sync"
	"testing"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/skill"
)

// ---------------------------------------------------------------------------
// mockDocStore — implements rag.DocumentStore for tests
// ---------------------------------------------------------------------------

type mockDocStore struct {
	mu           sync.Mutex
	searchCalled int
	lastQuery    string
	lastVec      []float32
	lastLimit    int
	results      []rag.SearchResult
	searchErr    error
}

func (m *mockDocStore) AddDocument(ctx context.Context, doc rag.Document) error { return nil }
func (m *mockDocStore) AddChunks(ctx context.Context, docID string, chunks []rag.DocumentChunk) error {
	return nil
}
func (m *mockDocStore) DeleteDocument(ctx context.Context, docID string) error { return nil }
func (m *mockDocStore) ListDocuments(ctx context.Context, namespace string) ([]rag.Document, error) {
	return nil, nil
}
func (m *mockDocStore) SearchChunks(ctx context.Context, query string, queryVec []float32, limit int) ([]rag.SearchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchCalled++
	m.lastQuery = query
	m.lastVec = queryVec
	m.lastLimit = limit
	return m.results, m.searchErr
}

func (m *mockDocStore) searchCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.searchCalled
}

// ---------------------------------------------------------------------------
// T6.2 — Agent with ragStore → SearchChunks called per turn
// ---------------------------------------------------------------------------

func TestWithRAGStore_SearchCalledPerTurn(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{{Content: "hello"}},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	docStore := &mockDocStore{
		results: []rag.SearchResult{
			makeSearchResult("TestDoc", 0, "relevant content"),
		},
	}
	ag.WithRAGStore(docStore, nil, 5, 10000)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c1",
		Content:   content.TextBlock("what is testing?"),
	})

	if docStore.searchCallCount() == 0 {
		t.Error("expected SearchChunks to be called at least once, got 0")
	}
}

func TestWithRAGStore_QueryMatchesUserMessage(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{{Content: "answer"}},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	docStore := &mockDocStore{}
	ag.WithRAGStore(docStore, nil, 3, 10000)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c1",
		Content:   content.TextBlock("unique query text"),
	})

	docStore.mu.Lock()
	q := docStore.lastQuery
	docStore.mu.Unlock()

	if q != "unique query text" {
		t.Errorf("expected query 'unique query text', got %q", q)
	}
}

func TestWithRAGStore_ResultsInjectedIntoSystemPrompt(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	inner := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	prov := &capturingProvider{inner: inner}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	docStore := &mockDocStore{
		results: []rag.SearchResult{
			makeSearchResult("MarkerDoc", 0, "INJECTED_RAG_MARKER"),
		},
	}
	ag.WithRAGStore(docStore, nil, 5, 10000)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c1",
		Content:   content.TextBlock("test"),
	})

	sp := prov.lastReq.SystemPrompt
	if !containsStr(sp, "INJECTED_RAG_MARKER") {
		t.Errorf("expected RAG marker in system prompt, got:\n%s", sp)
	}
}

func TestWithRAGStore_NilStore_NoSearchNoBehaviorChange(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{{Content: "hi"}},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	// Do NOT call WithRAGStore — ragStore should be nil.

	// Should not panic.
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c1",
		Content:   content.TextBlock("hello"),
	})

	if len(ch.sent) == 0 {
		t.Error("expected a reply even without RAG store")
	}
}

func TestWithRAGStore_EmbedFnCalledWhenSet(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context:          config.ContextConfig{Strategy: "none"},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{{Content: "response"}},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	var embedCalled int
	var embedMu sync.Mutex
	embedFn := func(ctx context.Context, text string) ([]float32, error) {
		embedMu.Lock()
		embedCalled++
		embedMu.Unlock()
		return []float32{0.1, 0.2}, nil
	}

	docStore := &mockDocStore{}
	ag.WithRAGStore(docStore, embedFn, 5, 10000)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c1",
		Content:   content.TextBlock("embedding test"),
	})

	embedMu.Lock()
	called := embedCalled
	embedMu.Unlock()

	if called == 0 {
		t.Error("expected embedFn to be called at least once")
	}

	docStore.mu.Lock()
	vec := docStore.lastVec
	docStore.mu.Unlock()

	if len(vec) == 0 {
		t.Error("expected non-empty queryVec passed to SearchChunks")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func containsStr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
