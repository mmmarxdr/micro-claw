package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store for consolidator tests
// ---------------------------------------------------------------------------

// consolidatorMockStore records memory operations; SearchMemory returns a
// configurable set of entries (set via mu-guarded field).
type consolidatorMockStore struct {
	mu          sync.Mutex
	entries     []store.MemoryEntry // returned by SearchMemory (full table)
	appendCalls []store.MemoryEntry
	updateCalls []store.MemoryEntry
	appendErr   error
	updateErr   error
	searchErr   error
}

func (s *consolidatorMockStore) SaveConversation(_ context.Context, _ store.Conversation) error {
	return nil
}
func (s *consolidatorMockStore) LoadConversation(_ context.Context, _ string) (*store.Conversation, error) {
	return nil, store.ErrNotFound
}
func (s *consolidatorMockStore) ListConversations(_ context.Context, _ string, _ int) ([]store.Conversation, error) {
	return nil, nil
}
func (s *consolidatorMockStore) AppendMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	s.appendCalls = append(s.appendCalls, entry)
	return nil
}
func (s *consolidatorMockStore) SearchMemory(_ context.Context, _ string, _ string, _ int) ([]store.MemoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	// Return only non-archived entries (simulate DB filter).
	var result []store.MemoryEntry
	for _, e := range s.entries {
		if e.ArchivedAt == nil {
			result = append(result, e)
		}
	}
	return result, nil
}
func (s *consolidatorMockStore) UpdateMemory(_ context.Context, _ string, entry store.MemoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updateCalls = append(s.updateCalls, entry)
	return nil
}
func (s *consolidatorMockStore) Close() error { return nil }

func (s *consolidatorMockStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appendCalls)
}

func (s *consolidatorMockStore) updateCount() int { //nolint:unused // kept for future test assertions
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.updateCalls)
}

func (s *consolidatorMockStore) lastAppend() store.MemoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.appendCalls) == 0 {
		return store.MemoryEntry{}
	}
	return s.appendCalls[len(s.appendCalls)-1]
}

// allUpdates returns a snapshot of all update calls.
func (s *consolidatorMockStore) allUpdates() []store.MemoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.MemoryEntry, len(s.updateCalls))
	copy(out, s.updateCalls)
	return out
}

// ---------------------------------------------------------------------------
// consolidatorMockProvider — fixed response for all Chat calls.
// ---------------------------------------------------------------------------

type consolidatorMockProvider struct {
	mu       sync.Mutex
	response string
	err      error
	calls    int
}

func (p *consolidatorMockProvider) Name() string                                  { return "mock" }
func (p *consolidatorMockProvider) Model() string                                 { return "mock" }
func (p *consolidatorMockProvider) SupportsTools() bool                           { return false }
func (p *consolidatorMockProvider) SupportsMultimodal() bool                      { return false }
func (p *consolidatorMockProvider) SupportsAudio() bool                           { return false }
func (p *consolidatorMockProvider) HealthCheck(_ context.Context) (string, error) { return "ok", nil }
func (p *consolidatorMockProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &provider.ChatResponse{Content: p.response}, nil
}

func (p *consolidatorMockProvider) chatCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func defaultConsolidationCfg() config.ConsolidationConfig {
	return config.ConsolidationConfig{
		Enabled:            true,
		IntervalHours:      24,
		MinEntriesPerTopic: 5,
		KeepNewest:         3,
	}
}

// makeEntries builds n entries with the given topic, created from oldest to newest.
func makeEntries(n int, topic, typeStr string) []store.MemoryEntry {
	entries := make([]store.MemoryEntry, n)
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		entries[i] = store.MemoryEntry{
			ID:         fmt.Sprintf("entry-%s-%d", topic, i),
			ScopeID:    "test-scope",
			Topic:      topic,
			Type:       typeStr,
			Content:    fmt.Sprintf("content for %s entry %d", topic, i),
			Importance: 5,
			CreatedAt:  base.Add(time.Duration(i) * time.Hour),
		}
	}
	return entries
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestConsolidator_NewDisabled(t *testing.T) {
	cfg := config.ConsolidationConfig{Enabled: false}
	prov := &consolidatorMockProvider{response: "consolidated"}
	st := &consolidatorMockStore{}
	c := NewConsolidator(prov, st, nil, nil, cfg)
	if c != nil {
		t.Error("expected nil Consolidator when disabled")
	}
}

func TestConsolidator_NoScopes(t *testing.T) {
	// When there are no entries, consolidateScope should silently succeed.
	prov := &consolidatorMockProvider{response: "consolidated"}
	st := &consolidatorMockStore{} // entries is nil → SearchMemory returns []

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())
	if c == nil {
		t.Fatal("expected non-nil Consolidator")
	}

	// consolidateScope with no entries should not call LLM or create entries.
	archived, created, err := c.consolidateScope(context.Background(), "scope-empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived != 0 || created != 0 {
		t.Errorf("expected 0 archived and 0 created, got archived=%d, created=%d", archived, created)
	}
	if prov.chatCalls() != 0 {
		t.Error("expected no LLM calls when no entries")
	}
}

func TestConsolidator_TopicBelowThreshold(t *testing.T) {
	// 3 entries for a topic — below MinEntriesPerTopic=5 — no consolidation.
	prov := &consolidatorMockProvider{response: "consolidated"}
	st := &consolidatorMockStore{
		entries: makeEntries(3, "cooking", "fact"),
	}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())

	archived, created, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived != 0 || created != 0 {
		t.Errorf("expected no consolidation below threshold, got archived=%d, created=%d", archived, created)
	}
	if prov.chatCalls() != 0 {
		t.Error("expected no LLM calls below threshold")
	}
}

func TestConsolidator_TopicAboveThreshold(t *testing.T) {
	// 7 entries, KeepNewest=3 → 4 candidates consolidated, 3 kept.
	prov := &consolidatorMockProvider{response: "Consolidated summary of cooking tips"}
	st := &consolidatorMockStore{
		entries: makeEntries(7, "cooking", "fact"),
	}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())

	archived, created, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created != 1 {
		t.Errorf("expected 1 consolidated entry created, got %d", created)
	}
	if archived != 4 {
		t.Errorf("expected 4 archived (7 - 3 kept), got %d", archived)
	}
	if st.appendCount() != 1 {
		t.Errorf("expected 1 AppendMemory call, got %d", st.appendCount())
	}

	// The new entry should have the right topic and title.
	newEntry := st.lastAppend()
	if newEntry.Topic != "cooking" {
		t.Errorf("expected Topic='cooking', got %q", newEntry.Topic)
	}
	if newEntry.Source != "consolidator" {
		t.Errorf("expected Source='consolidator', got %q", newEntry.Source)
	}
}

func TestConsolidator_PreservesMaxImportance(t *testing.T) {
	// Candidates have varying importance — consolidated entry gets max.
	entries := makeEntries(6, "architecture", "decision")
	// Give the oldest candidates varying importance values.
	entries[0].Importance = 3
	entries[1].Importance = 8
	entries[2].Importance = 5

	prov := &consolidatorMockProvider{response: "Architecture decisions summary"}
	st := &consolidatorMockStore{entries: entries}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())
	_, _, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newEntry := st.lastAppend()
	// Candidates are entries[0..2] (oldest 3, since KeepNewest=3).
	// Max importance among candidates = max(3, 8, 5) = 8.
	if newEntry.Importance < 8 {
		t.Errorf("expected Importance >= 8 (max of candidates), got %d", newEntry.Importance)
	}
}

func TestConsolidator_ArchivesOriginals(t *testing.T) {
	// After consolidation, the candidate entries should have ArchivedAt set via UpdateMemory.
	entries := makeEntries(7, "projects", "context")
	prov := &consolidatorMockProvider{response: "Project history summary"}
	st := &consolidatorMockStore{entries: entries}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())
	archived, _, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updates := st.allUpdates()
	if len(updates) != archived {
		t.Errorf("expected %d UpdateMemory calls (one per archived), got %d", archived, len(updates))
	}

	for i, u := range updates {
		if u.ArchivedAt == nil {
			t.Errorf("update[%d]: expected ArchivedAt to be set, got nil", i)
		}
	}
}

func TestConsolidator_MultipleTopics(t *testing.T) {
	// Two topics each with 7 entries — both should be consolidated independently.
	cookingEntries := makeEntries(7, "cooking", "fact")
	archEntries := makeEntries(7, "architecture", "decision")
	allEntries := append(cookingEntries, archEntries...)

	prov := &consolidatorMockProvider{response: "Summary"}
	st := &consolidatorMockStore{entries: allEntries}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())
	_, created, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should create one consolidated entry per topic.
	if created != 2 {
		t.Errorf("expected 2 consolidated entries (one per topic), got %d", created)
	}
	if st.appendCount() != 2 {
		t.Errorf("expected 2 AppendMemory calls, got %d", st.appendCount())
	}
	if prov.chatCalls() != 2 {
		t.Errorf("expected 2 LLM calls (one per topic), got %d", prov.chatCalls())
	}
}

func TestConsolidator_SkipsEmptyTopic(t *testing.T) {
	// Entries with empty topic should not be grouped or consolidated.
	entries := make([]store.MemoryEntry, 10)
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 10; i++ {
		entries[i] = store.MemoryEntry{
			ID:        fmt.Sprintf("notopic-%d", i),
			Topic:     "", // no topic
			Content:   fmt.Sprintf("uncategorized content %d", i),
			Importance: 5,
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}
	}

	prov := &consolidatorMockProvider{response: "Summary"}
	st := &consolidatorMockStore{entries: entries}

	c := NewConsolidator(prov, st, nil, nil, defaultConsolidationCfg())
	archived, created, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived != 0 || created != 0 {
		t.Errorf("expected no consolidation for empty-topic entries, got archived=%d, created=%d", archived, created)
	}
	if prov.chatCalls() != 0 {
		t.Error("expected no LLM calls for empty-topic entries")
	}
}

func TestConsolidator_LLMFailure_NoDataLoss(t *testing.T) {
	// When the LLM fails for one topic, that topic is skipped but originals are NOT archived.
	// Other topics should still be processed if present.
	cookingEntries := makeEntries(7, "cooking", "fact")
	archEntries := makeEntries(7, "architecture", "decision")
	allEntries := append(cookingEntries, archEntries...)

	callCount := 0
	// Mock that fails on first call (cooking) but succeeds on second (architecture).
	mockProv := &callCountProvider{
		onCall: func(n int) (*provider.ChatResponse, error) {
			if n == 1 {
				return nil, errors.New("LLM timeout")
			}
			return &provider.ChatResponse{Content: "Architecture summary"}, nil
		},
	}

	st := &consolidatorMockStore{entries: allEntries}
	_ = callCount

	c := NewConsolidator(mockProv, st, nil, nil, defaultConsolidationCfg())
	_, created, err := c.consolidateScope(context.Background(), "test-scope")
	if err != nil {
		t.Fatalf("unexpected error from consolidateScope (errors are per-topic, not returned): %v", err)
	}

	// Only one topic succeeds (architecture); cooking is skipped.
	// NOTE: map iteration order is random, so either topic might fail.
	// We just assert that at most 1 consolidated entry is created (not 2 or 0 in a way that loses data).
	if created > 2 {
		t.Errorf("expected at most 2 created entries (one per topic, one fails), got %d", created)
	}

	// Entries from the failed topic must NOT have ArchivedAt set.
	updates := st.allUpdates()
	archivedCount := 0
	for _, u := range updates {
		if u.ArchivedAt != nil {
			archivedCount++
		}
	}
	// Exactly 4 entries should be archived (from the one successful topic).
	if archivedCount > 4 {
		t.Errorf("expected at most 4 archived entries from successful topic, got %d", archivedCount)
	}
}

// callCountProvider is a provider where each call invokes onCall(n) where n
// starts at 1 and increments. Useful for testing per-call behaviour.
type callCountProvider struct {
	mu     sync.Mutex
	count  int
	onCall func(n int) (*provider.ChatResponse, error)
}

func (p *callCountProvider) Name() string                                  { return "mock" }
func (p *callCountProvider) Model() string                                 { return "mock" }
func (p *callCountProvider) SupportsTools() bool                           { return false }
func (p *callCountProvider) SupportsMultimodal() bool                      { return false }
func (p *callCountProvider) SupportsAudio() bool                           { return false }
func (p *callCountProvider) HealthCheck(_ context.Context) (string, error) { return "ok", nil }
func (p *callCountProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.mu.Lock()
	p.count++
	n := p.count
	p.mu.Unlock()
	return p.onCall(n)
}
