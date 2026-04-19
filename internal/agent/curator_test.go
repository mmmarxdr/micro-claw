package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Mock provider for curator tests
// ---------------------------------------------------------------------------

// curatorMockProvider returns a configurable Chat response or error.
// It records every call so tests can assert call counts.
type curatorMockProvider struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
}

func (p *curatorMockProvider) Name() string                                  { return "mock" }
func (p *curatorMockProvider) Model() string                                 { return "mock-model" }
func (p *curatorMockProvider) SupportsTools() bool                           { return false }
func (p *curatorMockProvider) SupportsMultimodal() bool                      { return false }
func (p *curatorMockProvider) SupportsAudio() bool                           { return false }
func (p *curatorMockProvider) HealthCheck(_ context.Context) (string, error) { return "ok", nil }
func (p *curatorMockProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &provider.ChatResponse{Content: p.response}, nil
}

func (p *curatorMockProvider) chatCalls() int { //nolint:unused // kept for future test assertions
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ---------------------------------------------------------------------------
// Mock store for curator tests
// ---------------------------------------------------------------------------

// curatorMockStore records AppendMemory and UpdateMemory calls.
// SearchMemory returns a configurable set of candidates.
type curatorMockStore struct {
	mu           sync.Mutex
	appendCalls  []store.MemoryEntry
	updateCalls  []store.MemoryEntry
	candidates   []store.MemoryEntry // returned by SearchMemory
	searchErr    error
	appendErr    error
}

func (s *curatorMockStore) SaveConversation(_ context.Context, _ store.Conversation) error {
	return nil
}
func (s *curatorMockStore) LoadConversation(_ context.Context, _ string) (*store.Conversation, error) {
	return nil, store.ErrNotFound
}
func (s *curatorMockStore) ListConversations(_ context.Context, _ string, _ int) ([]store.Conversation, error) {
	return nil, nil
}
func (s *curatorMockStore) AppendMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	s.appendCalls = append(s.appendCalls, entry)
	return nil
}
func (s *curatorMockStore) SearchMemory(_ context.Context, _ string, _ string, _ int) ([]store.MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.candidates, nil
}
func (s *curatorMockStore) UpdateMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls = append(s.updateCalls, entry)
	return nil
}
func (s *curatorMockStore) Close() error { return nil }

func (s *curatorMockStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appendCalls)
}

func (s *curatorMockStore) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updateCalls)
}

func (s *curatorMockStore) lastAppend() store.MemoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.appendCalls) == 0 {
		return store.MemoryEntry{}
	}
	return s.appendCalls[len(s.appendCalls)-1]
}

func (s *curatorMockStore) lastUpdate() store.MemoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updateCalls) == 0 {
		return store.MemoryEntry{}
	}
	return s.updateCalls[len(s.updateCalls)-1]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func enabledCurationCfg() config.MemoryCurationConfig {
	return config.MemoryCurationConfig{
		Enabled:          true,
		MinImportance:    5,
		MinResponseChars: 50,
	}
}

func enabledDedupCfg() config.DeduplicationConfig { //nolint:unused // kept for future test assertions
	return config.DeduplicationConfig{
		Enabled:         true,
		CosineThreshold: 0.85,
		MaxCandidates:   5,
	}
}

func disabledDedupCfg() config.DeduplicationConfig {
	return config.DeduplicationConfig{Enabled: false}
}

// classifyJSON returns a well-formed JSON classification string for tests.
func classifyJSON(importance int, typ, topic, title string) string {
	return fmt.Sprintf(`{"importance":%d,"type":%q,"topic":%q,"title":%q}`,
		importance, typ, topic, title)
}

// longResponse returns a string of n repeated characters — useful to exceed
// the MinResponseChars threshold without crafting real content.
func longResponse(n int) string {
	return strings.Repeat("x", n)
}

// ---------------------------------------------------------------------------
// Test 1: NewCurator returns nil when Enabled = false
// ---------------------------------------------------------------------------

func TestCurator_NewCurator_Disabled(t *testing.T) {
	prov := &curatorMockProvider{}
	st := &curatorMockStore{}
	cfg := config.MemoryCurationConfig{Enabled: false}

	c := NewCurator(prov, st, nil, nil, cfg, disabledDedupCfg())
	if c != nil {
		t.Fatal("expected nil Curator when Enabled=false, got non-nil")
	}
}

// ---------------------------------------------------------------------------
// Test 2: shouldSkip — short response
// ---------------------------------------------------------------------------

func TestCurator_ShouldSkip_ShortResponse(t *testing.T) {
	prov := &curatorMockProvider{response: classifyJSON(8, "fact", "topic", "title")}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	// 49 chars — below MinResponseChars (50).
	if !c.shouldSkip(strings.Repeat("a", 49)) {
		t.Fatal("expected shouldSkip=true for 49-char response")
	}

	// Exactly 50 chars should NOT be skipped.
	if c.shouldSkip(strings.Repeat("a", 50)) {
		t.Fatal("expected shouldSkip=false for 50-char response")
	}
}

// ---------------------------------------------------------------------------
// Test 3: shouldSkip — refusal prefix
// ---------------------------------------------------------------------------

func TestCurator_ShouldSkip_Refusal(t *testing.T) {
	prov := &curatorMockProvider{}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	refusals := []string{
		"I'm sorry, I cannot help with that request. " + longResponse(50),
		"I cannot assist with that. " + longResponse(50),
		"I can't do that. " + longResponse(50),
		"I don't have access to that information. " + longResponse(50),
		"Lo siento, no puedo ayudarte con eso. " + longResponse(20),
		"No puedo responder esa pregunta. " + longResponse(30),
	}

	for _, r := range refusals {
		if !c.shouldSkip(r) {
			t.Errorf("expected shouldSkip=true for refusal: %q", r[:40])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 4: shouldSkip — filler
// ---------------------------------------------------------------------------

func TestCurator_ShouldSkip_Filler(t *testing.T) {
	prov := &curatorMockProvider{}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	fillers := []string{"ok", "sure", "done", "understood", "got it", "Great!", "Thanks.", "OK!"}
	for _, f := range fillers {
		if !c.shouldSkip(f) {
			t.Errorf("expected shouldSkip=true for filler: %q", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 5: shouldSkip — valid non-skippable response
// ---------------------------------------------------------------------------

func TestCurator_ShouldSkip_ValidResponse(t *testing.T) {
	prov := &curatorMockProvider{}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	valid := "The user prefers Go for all backend services and wants to avoid Python in new projects."
	if c.shouldSkip(valid) {
		t.Fatal("expected shouldSkip=false for substantive response")
	}
}

// ---------------------------------------------------------------------------
// Test 6: classify — success with mock provider
// ---------------------------------------------------------------------------

func TestCurator_Classify_Success(t *testing.T) {
	prov := &curatorMockProvider{
		response: classifyJSON(8, "preference", "technology", "User prefers Go for backend"),
	}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	result, err := c.classify(context.Background(), "what language do you prefer?", longResponse(100))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Importance != 8 {
		t.Errorf("expected importance=8, got %d", result.Importance)
	}
	if result.Type != "preference" {
		t.Errorf("expected type=preference, got %q", result.Type)
	}
	if result.Topic != "technology" {
		t.Errorf("expected topic=technology, got %q", result.Topic)
	}
	if result.Title != "User prefers Go for backend" {
		t.Errorf("expected title set correctly, got %q", result.Title)
	}
}

// ---------------------------------------------------------------------------
// Test 7: classify — parse error falls back to defaults
// ---------------------------------------------------------------------------

func TestCurator_Classify_ParseError_Fallback(t *testing.T) {
	prov := &curatorMockProvider{response: "this is not json at all"}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	response := longResponse(100)
	result, err := c.classify(context.Background(), "user msg", response)
	// Parse error → nil error (fallback is used silently), importance=5.
	if err != nil {
		t.Fatalf("expected nil error on parse fallback, got %v", err)
	}
	if result.Importance != 5 {
		t.Errorf("expected fallback importance=5, got %d", result.Importance)
	}
	if result.Type != "context" {
		t.Errorf("expected fallback type=context, got %q", result.Type)
	}
}

// ---------------------------------------------------------------------------
// Test 8: Curate — low importance → not saved
// ---------------------------------------------------------------------------

func TestCurator_Curate_LowImportance_NotSaved(t *testing.T) {
	prov := &curatorMockProvider{
		response: classifyJSON(2, "skip", "irrelevant", "trivial"),
	}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	// 100+ char response so it passes the fast-path skip.
	err := c.Curate(context.Background(), "scope-1", "hello", longResponse(100), "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.appendCount() != 0 {
		t.Errorf("expected 0 appends for low-importance response, got %d", st.appendCount())
	}
}

// ---------------------------------------------------------------------------
// Test 9: Curate — high importance → saved with metadata
// ---------------------------------------------------------------------------

func TestCurator_Curate_HighImportance_Saved(t *testing.T) {
	prov := &curatorMockProvider{
		response: classifyJSON(8, "preference", "technology", "Prefers Go for backend"),
	}
	st := &curatorMockStore{}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), disabledDedupCfg())

	userMsg := "What language do you use for backend?"
	response := "I always use Go for backend services because of its performance and simplicity. Python is reserved for data science tasks."

	err := c.Curate(context.Background(), "scope-1", userMsg, response, "conv-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.appendCount() != 1 {
		t.Fatalf("expected 1 append for high-importance response, got %d", st.appendCount())
	}

	saved := st.lastAppend()
	if saved.Importance != 8 {
		t.Errorf("expected saved importance=8, got %d", saved.Importance)
	}
	if saved.Type != "preference" {
		t.Errorf("expected saved type=preference, got %q", saved.Type)
	}
	if saved.Topic != "technology" {
		t.Errorf("expected saved topic=technology, got %q", saved.Topic)
	}
	if saved.Title != "Prefers Go for backend" {
		t.Errorf("expected saved title='Prefers Go for backend', got %q", saved.Title)
	}
	if saved.ScopeID != "scope-1" {
		t.Errorf("expected ScopeID=scope-1, got %q", saved.ScopeID)
	}
	if saved.ID == "" {
		t.Error("expected non-empty entry ID")
	}
}

// ---------------------------------------------------------------------------
// Test 10: Curate — duplicate found → UpdateMemory called, no AppendMemory
// ---------------------------------------------------------------------------

func TestCurator_Curate_Dedup_UpdatesExisting(t *testing.T) {
	prov := &curatorMockProvider{
		response: classifyJSON(7, "fact", "cooking", "User likes pasta"),
	}

	existingContent := "The user enjoys cooking pasta dishes and making Italian food at home on weekends regularly."
	existingID := "existing-mem-id"

	st := &curatorMockStore{
		// Pre-seed a candidate with high Jaccard similarity to the incoming response.
		candidates: []store.MemoryEntry{
			{
				ID:      existingID,
				ScopeID: "scope-1",
				Content: existingContent,
				Topic:   "food",
				Type:    "fact",
			},
		},
	}

	dedupCfg := config.DeduplicationConfig{
		Enabled:         true,
		CosineThreshold: 0.85,
		MaxCandidates:   5,
	}
	c := NewCurator(prov, st, nil, nil, enabledCurationCfg(), dedupCfg)

	// Use the same content as the existing candidate to guarantee Jaccard > 0.7.
	err := c.Curate(context.Background(), "scope-1", "do you like pasta?", existingContent, "conv-2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.appendCount() != 0 {
		t.Errorf("expected 0 AppendMemory calls (dedup should update), got %d", st.appendCount())
	}
	if st.updateCount() != 1 {
		t.Fatalf("expected 1 UpdateMemory call for dedup, got %d", st.updateCount())
	}

	updated := st.lastUpdate()
	if updated.ID != existingID {
		t.Errorf("expected UpdateMemory called with existing ID %q, got %q", existingID, updated.ID)
	}
}
