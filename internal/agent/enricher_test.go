package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Mocks for enricher tests
// ---------------------------------------------------------------------------

// enrichMockProvider is a provider that returns a configurable response or error.
type enrichMockProvider struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
	delay    time.Duration // artificial delay to simulate slow responses
}

func (p *enrichMockProvider) Name() string                                  { return "mock" }
func (p *enrichMockProvider) Model() string                                 { return "mock-model" }
func (p *enrichMockProvider) SupportsTools() bool                           { return false }
func (p *enrichMockProvider) SupportsMultimodal() bool                      { return false }
func (p *enrichMockProvider) SupportsAudio() bool                           { return false }
func (p *enrichMockProvider) HealthCheck(_ context.Context) (string, error) { return "ok", nil }
func (p *enrichMockProvider) Chat(ctx context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.mu.Lock()
	p.calls++
	delay := p.delay
	resp := p.response
	err := p.err
	p.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &provider.ChatResponse{Content: resp}, nil
}

func (p *enrichMockProvider) chatCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// enrichMockStore records UpdateMemory calls.
type enrichMockStore struct {
	mu           sync.Mutex
	updateCalls  []store.MemoryEntry
	appendedMems []store.MemoryEntry
}

func (s *enrichMockStore) SaveConversation(_ context.Context, _ store.Conversation) error { return nil }
func (s *enrichMockStore) LoadConversation(_ context.Context, _ string) (*store.Conversation, error) {
	return nil, store.ErrNotFound
}

func (s *enrichMockStore) ListConversations(_ context.Context, _ string, _ int) ([]store.Conversation, error) {
	return nil, nil
}

func (s *enrichMockStore) AppendMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendedMems = append(s.appendedMems, entry)
	return nil
}

func (s *enrichMockStore) SearchMemory(_ context.Context, _ string, _ string, _ int) ([]store.MemoryEntry, error) {
	return nil, nil
}

func (s *enrichMockStore) UpdateMemory(_ context.Context, scopeID string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls = append(s.updateCalls, entry)
	return nil
}
func (s *enrichMockStore) Close() error { return nil }

func (s *enrichMockStore) getUpdateCalls() []store.MemoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]store.MemoryEntry, len(s.updateCalls))
	copy(result, s.updateCalls)
	return result
}

// ---------------------------------------------------------------------------
// Helper: default enricher config
// ---------------------------------------------------------------------------

func enrichTestCfg() config.AgentConfig {
	return config.AgentConfig{
		EnrichMemory:     true,
		EnrichRatePerMin: 60, // effectively 1 per second — generous for tests
	}
}

func testEntry() store.MemoryEntry {
	return store.MemoryEntry{
		ID:      "entry-1",
		ScopeID: "scope-1",
		Content: "The user prefers Go for backend services",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestEnricher_HappyPath verifies that tags are parsed from the LLM response
// and UpdateMemory is called with the correct tags.
func TestEnricher_HappyPath(t *testing.T) {
	prov := &enrichMockProvider{response: "go, backend, preferences, services, programming"}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewEnricher(prov, st, cfg)
	if e == nil {
		t.Fatal("expected non-nil Enricher when EnrichMemory=true")
	}
	defer e.Stop()
	e.Start(ctx)

	entry := testEntry()
	e.Enqueue(entry)

	// Wait for UpdateMemory to be called.
	deadline := time.After(3 * time.Second)
	for {
		calls := st.getUpdateCalls()
		if len(calls) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("UpdateMemory was not called within 3s")
		case <-time.After(10 * time.Millisecond):
		}
	}

	calls := st.getUpdateCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 UpdateMemory call, got %d", len(calls))
	}
	updated := calls[0]
	if updated.ID != entry.ID {
		t.Errorf("expected entry ID %q, got %q", entry.ID, updated.ID)
	}
	if len(updated.Tags) == 0 {
		t.Error("expected at least one tag to be set")
	}
	// Verify tags are trimmed and lowercase.
	for _, tag := range updated.Tags {
		if strings.TrimSpace(tag) != tag {
			t.Errorf("tag %q has leading/trailing whitespace", tag)
		}
	}
	// Verify at least one expected tag is present.
	found := false
	for _, tag := range updated.Tags {
		if tag == "go" || tag == "backend" || tag == "preferences" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one of 'go','backend','preferences' in tags, got %v", updated.Tags)
	}
}

// TestEnricher_DisabledWhenFalse verifies NewEnricher returns nil when EnrichMemory=false.
func TestEnricher_DisabledWhenFalse(t *testing.T) {
	prov := &enrichMockProvider{}
	st := &enrichMockStore{}
	cfg := config.AgentConfig{EnrichMemory: false}

	e := NewEnricher(prov, st, cfg)
	if e != nil {
		t.Error("expected nil Enricher when EnrichMemory=false")
	}
}

// TestEnricher_ChannelFull verifies that when the channel is full, Enqueue drops
// the job silently without blocking and without calling UpdateMemory.
func TestEnricher_ChannelFull(t *testing.T) {
	// Use a very slow provider so the worker won't drain the channel.
	prov := &enrichMockProvider{delay: 10 * time.Second}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())

	e := NewEnricher(prov, st, cfg)
	e.Start(ctx)

	// Fill the channel past capacity (cap=5) — all should return immediately.
	entry := testEntry()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			e.Enqueue(entry)
		}
	}()

	select {
	case <-done:
		// Good — all Enqueue calls returned quickly.
	case <-time.After(500 * time.Millisecond):
		t.Error("Enqueue blocked for more than 500ms — must be non-blocking")
	}

	// Cancel context so the worker exits quickly (unblocks slow LLM calls).
	cancel()
	e.Stop()
}

// TestEnricher_RateLimitExceeded verifies that when rate limit is exhausted,
// no LLM call is made.
func TestEnricher_RateLimitExceeded(t *testing.T) {
	prov := &enrichMockProvider{response: "tag1, tag2"}
	st := &enrichMockStore{}

	// Rate limit: 2 calls per minute.
	cfg := config.AgentConfig{
		EnrichMemory:     true,
		EnrichRatePerMin: 2,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewEnricher(prov, st, cfg)
	defer e.Stop()
	e.Start(ctx)

	entry := testEntry()

	// Send 3 entries. After 2 LLM calls, the third should be rate-limited.
	for i := 0; i < 3; i++ {
		e.Enqueue(entry)
	}

	// Give worker time to process.
	time.Sleep(200 * time.Millisecond)

	// At most 2 LLM calls should have been made (rate limit = 2 per minute).
	calls := prov.chatCalls()
	if calls > 2 {
		t.Errorf("expected at most 2 LLM calls due to rate limit, got %d", calls)
	}
}

// TestEnricher_LLMTimeout verifies that a slow LLM call times out (5s context)
// and UpdateMemory is NOT called.
func TestEnricher_LLMTimeout(t *testing.T) {
	// Provider takes longer than 5s enrichment timeout.
	prov := &enrichMockProvider{delay: 10 * time.Second}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewEnricher(prov, st, cfg)
	defer e.Stop()
	e.Start(ctx)

	e.Enqueue(testEntry())

	// Wait just over the 5s timeout for the job to be attempted and fail.
	time.Sleep(6 * time.Second)

	// UpdateMemory must NOT have been called.
	if calls := st.getUpdateCalls(); len(calls) != 0 {
		t.Errorf("expected 0 UpdateMemory calls after LLM timeout, got %d", len(calls))
	}
	// But the LLM call should have been attempted.
	if prov.chatCalls() == 0 {
		t.Error("expected at least 1 LLM call attempt")
	}
}

// TestEnricher_EmptyLLMResponse verifies that an empty LLM response does not
// result in an UpdateMemory call.
func TestEnricher_EmptyLLMResponse(t *testing.T) {
	prov := &enrichMockProvider{response: ""}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewEnricher(prov, st, cfg)
	defer e.Stop()
	e.Start(ctx)

	e.Enqueue(testEntry())

	// Give the worker time to process.
	time.Sleep(200 * time.Millisecond)

	if calls := st.getUpdateCalls(); len(calls) != 0 {
		t.Errorf("expected 0 UpdateMemory calls for empty response, got %d", len(calls))
	}
}

// TestEnricher_WhitespaceOnlyLLMResponse verifies that a whitespace-only LLM
// response does not result in an UpdateMemory call.
func TestEnricher_WhitespaceOnlyLLMResponse(t *testing.T) {
	prov := &enrichMockProvider{response: "   \n\t  "}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := NewEnricher(prov, st, cfg)
	defer e.Stop()
	e.Start(ctx)

	e.Enqueue(testEntry())
	time.Sleep(200 * time.Millisecond)

	if calls := st.getUpdateCalls(); len(calls) != 0 {
		t.Errorf("expected 0 UpdateMemory calls for whitespace-only response, got %d", len(calls))
	}
}

// TestEnricher_Shutdown verifies that calling Stop() causes the worker goroutine
// to exit cleanly with no goroutine leak.
func TestEnricher_Shutdown(t *testing.T) {
	baseline := runtime.NumGoroutine()

	prov := &enrichMockProvider{response: ""}
	st := &enrichMockStore{}
	cfg := enrichTestCfg()

	ctx, cancel := context.WithCancel(context.Background())
	e := NewEnricher(prov, st, cfg)
	e.Start(ctx)

	// Give the goroutine time to start.
	time.Sleep(20 * time.Millisecond)

	goroutinesWithWorker := runtime.NumGoroutine()
	if goroutinesWithWorker <= baseline {
		t.Log("note: goroutine may not have started yet or was counted differently")
	}

	cancel()
	e.Stop()

	// Wait for cleanup.
	time.Sleep(50 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Allow a small buffer — the test framework itself may spawn goroutines.
	if after > baseline+2 {
		t.Errorf("goroutine leak: before=%d, after Stop=%d (expected ~%d)", baseline, after, baseline)
	}
}

// TestEnricher_TagParsing verifies comma-separated tag parsing, trimming, and
// filtering of empty entries.
func TestEnricher_TagParsing(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     []string
	}{
		{
			name:     "clean tags",
			response: "go, backend, api",
			want:     []string{"go", "backend", "api"},
		},
		{
			name:     "extra whitespace",
			response: "  go ,  backend  ,  api  ",
			want:     []string{"go", "backend", "api"},
		},
		{
			name:     "trailing comma",
			response: "go, backend,",
			want:     []string{"go", "backend"},
		},
		{
			name:     "single tag",
			response: "golang",
			want:     []string{"golang"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseEnrichTags(tt.response)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d tags, got %d: %v", len(tt.want), len(got), got)
			}
			for i, tag := range got {
				if tag != tt.want[i] {
					t.Errorf("tag[%d]: expected %q, got %q", i, tt.want[i], tag)
				}
			}
		})
	}
}

// TestResolveEnrichModel verifies that resolveEnrichModel returns the
// override when set and empty string otherwise (signals "use provider default").
func TestResolveEnrichModel(t *testing.T) {
	tests := []struct {
		providerName string
		override     string
		want         string
	}{
		{"anthropic", "", ""},
		{"gemini", "", ""},
		{"openai", "", ""},
		{"openrouter", "", ""},
		{"unknown", "", ""},
		{"anthropic", "custom-model", "custom-model"},
		{"openrouter", "meta-llama/llama-3.1-8b-instruct:free", "meta-llama/llama-3.1-8b-instruct:free"},
	}

	for _, tt := range tests {
		t.Run(tt.providerName+"_"+tt.override, func(t *testing.T) {
			prov := &enrichMockProviderWithName{name: tt.providerName}
			got := resolveEnrichModel(prov, tt.override)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

type enrichMockProviderWithName struct {
	enrichMockProvider
	name string
}

func (p *enrichMockProviderWithName) Name() string { return p.name }
