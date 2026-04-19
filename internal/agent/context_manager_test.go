package agent

import (
	"context"
	"strings"
	"testing"

	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/notify"
	"daimon/internal/provider"
)

// --- Mock provider helpers ---

// cmMockProvider is a minimal Provider for context manager tests.
type cmMockProvider struct {
	name  string
	model string
}

func (m *cmMockProvider) Name() string                                              { return m.name }
func (m *cmMockProvider) Model() string                                             { return m.model }
func (m *cmMockProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}
func (m *cmMockProvider) SupportsTools() bool                             { return false }
func (m *cmMockProvider) SupportsMultimodal() bool                        { return false }
func (m *cmMockProvider) SupportsAudio() bool                             { return false }
func (m *cmMockProvider) HealthCheck(_ context.Context) (string, error)   { return "ok", nil }

// cmMockModelLister embeds cmMockProvider and also implements ModelLister.
type cmMockModelLister struct {
	cmMockProvider
	models []provider.ModelInfo
	err    error
}

func (m *cmMockModelLister) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	return m.models, m.err
}

// --- T2.1 tests ---

func TestNewContextManager_AppliesDefaults(t *testing.T) {
	cfg := config.ContextConfig{} // all zero — defaults should be filled
	prov := &cmMockProvider{name: "test", model: "test-model"}

	cm := NewContextManager(cfg, prov, nil)

	if cm == nil {
		t.Fatal("expected non-nil ContextManager")
	}
	if cm.cfg.CompactThreshold == 0 {
		t.Error("expected CompactThreshold to be set to default")
	}
	if cm.cfg.CooldownTurns == 0 {
		t.Error("expected CooldownTurns to be set to default")
	}
	if cm.cfg.Strategy == "" {
		t.Error("expected Strategy to be set to default")
	}
	if cm.cfg.FallbackCtxSize == 0 {
		t.Error("expected FallbackCtxSize to be set to default")
	}
}

func TestResolveContextSize_UserOverride(t *testing.T) {
	// When ResolveMaxTokens() > 0, use that directly without querying model list.
	cfg := config.ContextConfig{MaxTokens: 50000}
	prov := &cmMockProvider{name: "test", model: "test-model"}

	cm := NewContextManager(cfg, prov, nil)

	if cm.MaxTokens() != 50000 {
		t.Errorf("MaxTokens() = %d, want 50000", cm.MaxTokens())
	}
}

func TestResolveContextSize_AutoDetectSuccess(t *testing.T) {
	// ModelLister returns a matching model — use its ContextLength.
	cfg := config.ContextConfig{
		FallbackCtxSize: 128000,
	}
	prov := &cmMockModelLister{
		cmMockProvider: cmMockProvider{name: "openrouter", model: "openai/gpt-4o"},
		models: []provider.ModelInfo{
			{ID: "openai/gpt-4o", ContextLength: 128000},
			{ID: "anthropic/claude-3-haiku", ContextLength: 200000},
		},
	}

	cm := NewContextManager(cfg, prov, nil)

	if cm.MaxTokens() != 128000 {
		t.Errorf("MaxTokens() = %d, want 128000", cm.MaxTokens())
	}
}

func TestResolveContextSize_AutoDetectFailure_UsesFallback(t *testing.T) {
	// ModelLister returns an error — fall back to FallbackCtxSize.
	cfg := config.ContextConfig{
		FallbackCtxSize: 64000,
	}
	prov := &cmMockModelLister{
		cmMockProvider: cmMockProvider{name: "test", model: "some-model"},
		err:          context.DeadlineExceeded,
	}

	cm := NewContextManager(cfg, prov, nil)

	if cm.MaxTokens() != 64000 {
		t.Errorf("MaxTokens() = %d, want fallback 64000", cm.MaxTokens())
	}
}

func TestResolveContextSize_AutoDetectModelNotFound_UsesFallback(t *testing.T) {
	// ModelLister succeeds but model ID not in list — fall back.
	cfg := config.ContextConfig{
		FallbackCtxSize: 32000,
	}
	prov := &cmMockModelLister{
		cmMockProvider: cmMockProvider{name: "test", model: "unknown-model"},
		models: []provider.ModelInfo{
			{ID: "other-model", ContextLength: 8192},
		},
	}

	cm := NewContextManager(cfg, prov, nil)

	if cm.MaxTokens() != 32000 {
		t.Errorf("MaxTokens() = %d, want fallback 32000", cm.MaxTokens())
	}
}

func TestResolveContextSize_NoModelLister_UsesFallback(t *testing.T) {
	// Provider does not implement ModelLister — fall back.
	cfg := config.ContextConfig{
		FallbackCtxSize: 16000,
	}
	prov := &cmMockProvider{name: "test", model: "some-model"}

	cm := NewContextManager(cfg, prov, nil)

	if cm.MaxTokens() != 16000 {
		t.Errorf("MaxTokens() = %d, want fallback 16000", cm.MaxTokens())
	}
}

func TestUsage_BreakdownAccuracy(t *testing.T) {
	cfg := config.ContextConfig{MaxTokens: 100000}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	systemPrompt := "You are a helpful assistant."
	messages := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock("Hello")},
		{Role: "assistant", Content: content.TextBlock("Hi there!")},
	}

	usage := cm.Usage(EstimateTokens(systemPrompt), messages)

	wantSystem := EstimateTokens(systemPrompt)
	wantMsgs := EstimateMessagesTokens(messages)
	wantTotal := wantSystem + wantMsgs
	wantPct := float64(wantTotal) / float64(100000) * 100

	if usage.SystemPrompt != wantSystem {
		t.Errorf("SystemPrompt = %d, want %d", usage.SystemPrompt, wantSystem)
	}
	if usage.Messages != wantMsgs {
		t.Errorf("Messages = %d, want %d", usage.Messages, wantMsgs)
	}
	if usage.Total != wantTotal {
		t.Errorf("Total = %d, want %d", usage.Total, wantTotal)
	}
	if usage.Max != 100000 {
		t.Errorf("Max = %d, want 100000", usage.Max)
	}
	if usage.UsagePercent != wantPct {
		t.Errorf("UsagePercent = %f, want %f", usage.UsagePercent, wantPct)
	}
}

func TestUsage_ZeroMaxTokens_NoPanic(t *testing.T) {
	// resolvedMaxToks = 0 when no fallback set and no override — should not divide by zero
	cfg := config.ContextConfig{MaxTokens: 0, FallbackCtxSize: 1} // fallback=1 so resolved > 0 post-default
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)
	// Just ensure no panic
	_ = cm.Usage(10, nil)
}

// --- T2.2 tests ---

// compactionCounter is a helper to track how many times compactPipeline was called.
// We verify it indirectly via the lastCompactTurn field.

func makeMessages(n int) []provider.ChatMessage {
	msgs := make([]provider.ChatMessage, n)
	for i := range msgs {
		msgs[i] = provider.ChatMessage{
			Role:    "user",
			Content: content.TextBlock("message content that is long enough to count tokens properly"),
		}
	}
	return msgs
}

func largeMessages(n, tokensEach int) []provider.ChatMessage {
	// Build messages with enough tokens to exceed threshold.
	// tokensEach * 4 chars per token ≈ desired size.
	text := make([]byte, tokensEach*4)
	for i := range text {
		text[i] = 'x'
	}
	msgs := make([]provider.ChatMessage, n)
	for i := range msgs {
		msgs[i] = provider.ChatMessage{
			Role:    "user",
			Content: content.TextBlock(string(text)),
		}
	}
	return msgs
}

func TestManage_StrategyNone_AlwaysUnchanged(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens: 1000,
		Strategy:  "none",
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// Even with messages that would exceed threshold, strategy "none" is unchanged.
	msgs := largeMessages(10, 200) // 200 tokens each × 10 = 2000 tokens > 1000 max
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	if len(result) != len(msgs) {
		t.Errorf("strategy none: got %d messages, want %d", len(result), len(msgs))
	}
	if cm.lastCompactTurn != 0 {
		t.Errorf("strategy none: lastCompactTurn = %d, want 0", cm.lastCompactTurn)
	}
}

func TestManage_BelowThreshold_NoCompaction(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        10000,
		CompactThreshold: 0.8,
		Strategy:         "smart",
		CooldownTurns:    3,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// Small messages — well below threshold.
	msgs := makeMessages(3)
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	if len(result) != len(msgs) {
		t.Errorf("below threshold: got %d messages, want %d", len(result), len(msgs))
	}
	if cm.lastCompactTurn != 0 {
		t.Errorf("below threshold: lastCompactTurn = %d, want 0 (no compaction)", cm.lastCompactTurn)
	}
}

func TestManage_AtThreshold_CompactionTriggered(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        1000,
		CompactThreshold: 0.5,
		Strategy:         "smart",
		CooldownTurns:    3,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// 600 tokens of messages > 50% of 1000 → trigger
	msgs := largeMessages(3, 200) // 3 × 200 tokens ≈ 600 tokens (plus overhead)

	result := cm.Manage(context.Background(), "short", nil, msgs)
	// After Manage, we expect lastCompactTurn to be set (compaction was triggered).
	if cm.lastCompactTurn == 0 {
		t.Error("at threshold: expected lastCompactTurn > 0 (compaction triggered)")
	}
	// Result should be valid (stub returns messages unchanged for now)
	_ = result
}

func TestManage_CooldownActive_BelowHardMax_Skipped(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        10000, // large window: hard max = 10000 tokens
		CompactThreshold: 0.1,  // threshold = 1000 tokens
		Strategy:         "smart",
		CooldownTurns:    5,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// Trigger first compaction: 4 messages × ~300 tokens = ~1200 tokens > 1000 threshold.
	// And 1200 << 10000 (well below hard max).
	msgs := largeMessages(4, 300)
	cm.Manage(context.Background(), "sys", nil, msgs)

	firstTurn := cm.lastCompactTurn
	if firstTurn == 0 {
		t.Fatal("expected first compaction to trigger")
	}

	// Second call within cooldown — should NOT compact again (below hard max).
	cm.Manage(context.Background(), "sys", nil, msgs)

	if cm.lastCompactTurn != firstTurn {
		t.Errorf("within cooldown: lastCompactTurn changed from %d to %d, want unchanged", firstTurn, cm.lastCompactTurn)
	}
}

func TestManage_CooldownExpired_CompactionTriggered(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        1000,
		CompactThreshold: 0.1, // very low threshold so every call would trigger
		Strategy:         "smart",
		CooldownTurns:    2,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	msgs := largeMessages(2, 100)

	// Turn 1: triggers compaction (lastCompactTurn = 1)
	cm.Manage(context.Background(), "sys", nil, msgs)
	firstTurn := cm.lastCompactTurn

	// Turn 2: within cooldown (turn - firstTurn < CooldownTurns=2 → skip)
	cm.Manage(context.Background(), "sys", nil, msgs)
	if cm.lastCompactTurn != firstTurn {
		t.Errorf("turn 2 within cooldown: should not compact again, got lastCompactTurn=%d", cm.lastCompactTurn)
	}

	// Turn 3: cooldown expired (turn 3 - lastCompact turn 1 = 2 >= CooldownTurns=2 → trigger)
	cm.Manage(context.Background(), "sys", nil, msgs)
	if cm.lastCompactTurn <= firstTurn {
		t.Errorf("turn 3 after cooldown: expected lastCompactTurn > %d, got %d", firstTurn, cm.lastCompactTurn)
	}
}

func TestManage_AboveHardMax_OverridesCooldown(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        100, // very small window
		CompactThreshold: 0.5,
		Strategy:         "smart",
		CooldownTurns:    100, // very long cooldown
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// First compaction to start cooldown.
	msgs := largeMessages(2, 50) // ~100+ tokens > 50% threshold
	cm.Manage(context.Background(), "sys", nil, msgs)
	firstTurn := cm.lastCompactTurn

	// Second call — cooldown is active (100 turns), but messages are at 100% → override.
	// 100+ tokens out of 100 max = 100%+ → hard max override.
	msgs2 := largeMessages(3, 50) // even more tokens
	cm.Manage(context.Background(), "sys", nil, msgs2)

	if cm.lastCompactTurn <= firstTurn {
		t.Errorf("hard max override: expected lastCompactTurn > %d, got %d", firstTurn, cm.lastCompactTurn)
	}
}

func TestManage_StrategyLegacy_CallsLegacyPath(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens: 10000,
		Strategy:  "legacy",
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	msgs := makeMessages(3)
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	// Legacy stub just returns messages unchanged.
	if len(result) != len(msgs) {
		t.Errorf("legacy: got %d messages, want %d", len(result), len(msgs))
	}
}

func TestForceCompact_BypassesThresholdAndCooldown(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens:        100000,
		CompactThreshold: 0.99, // near-impossible threshold
		Strategy:         "smart",
		CooldownTurns:    1000,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	// Manually put in a "recent" compaction to activate cooldown.
	cm.lastCompactTurn = 1
	cm.currentTurn = 2

	msgs := makeMessages(2) // tiny — well below threshold

	cm.ForceCompact(context.Background(), "sys", nil, msgs)

	// ForceCompact should have updated lastCompactTurn.
	if cm.lastCompactTurn <= 1 {
		t.Errorf("ForceCompact: expected lastCompactTurn > 1, got %d", cm.lastCompactTurn)
	}
}

func TestManage_TurnIncrements(t *testing.T) {
	cfg := config.ContextConfig{
		MaxTokens: 10000,
		Strategy:  "smart",
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	msgs := makeMessages(1)

	cm.Manage(context.Background(), "sys", nil, msgs)
	if cm.currentTurn != 1 {
		t.Errorf("after 1st Manage: currentTurn = %d, want 1", cm.currentTurn)
	}
	cm.Manage(context.Background(), "sys", nil, msgs)
	if cm.currentTurn != 2 {
		t.Errorf("after 2nd Manage: currentTurn = %d, want 2", cm.currentTurn)
	}
}

// Ensure tests build and run — check for notify.Bus usage (even if nil)
func TestNewContextManager_WithBus(t *testing.T) {
	cfg := config.ContextConfig{}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	var bus notify.Bus // nil interface
	cm := NewContextManager(cfg, prov, bus)
	if cm == nil {
		t.Fatal("expected non-nil ContextManager")
	}
}

// --- T5.1 tests: agent.context.compacted events ---

// captureBus is a minimal notify.Bus that captures emitted events synchronously.
type captureBus struct {
	events []notify.Event
}

func (b *captureBus) Emit(event notify.Event) {
	b.events = append(b.events, event)
}
func (b *captureBus) Subscribe(_ func(notify.Event)) {}
func (b *captureBus) Close()                         {}

// makeCompactingCM returns a ContextManager configured so that it will compact
// on the first Manage call (very small window, very low threshold).
func makeCompactingCM(bus notify.Bus, notify_ *bool) *ContextManager {
	cfg := config.ContextConfig{
		MaxTokens:        100,
		CompactThreshold: 0.1, // 10% = 10 tokens threshold — easy to exceed
		Strategy:         "smart",
		CooldownTurns:    0,
		Notify:           notify_,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	return NewContextManager(cfg, prov, bus)
}

func boolPtr(b bool) *bool { return &b }

func TestContextCompacted_EventEmitted_OnSmartManage(t *testing.T) {
	bus := &captureBus{}
	cm := makeCompactingCM(bus, nil) // nil Notify → defaults to true

	// Messages big enough to exceed the tiny threshold
	msgs := largeMessages(5, 50)
	cm.Manage(context.Background(), "sys", nil, msgs)

	if len(bus.events) == 0 {
		t.Fatal("expected at least one event to be emitted after compaction, got none")
	}

	ev := bus.events[0]
	if ev.Type != notify.EventContextCompacted {
		t.Errorf("event type = %q, want %q", ev.Type, notify.EventContextCompacted)
	}
	if ev.Text == "" {
		t.Error("event Text should not be empty")
	}
	if _, ok := ev.Meta["tokens_before"]; !ok {
		t.Error("event Meta missing 'tokens_before'")
	}
	if _, ok := ev.Meta["tokens_after"]; !ok {
		t.Error("event Meta missing 'tokens_after'")
	}
	if _, ok := ev.Meta["turns_summarized"]; !ok {
		t.Error("event Meta missing 'turns_summarized'")
	}
}

func TestContextCompacted_NilBus_NoPanic(t *testing.T) {
	// bus is nil — should not panic
	cm := makeCompactingCM(nil, nil)
	msgs := largeMessages(5, 50)
	// Just ensure no panic
	_ = cm.Manage(context.Background(), "sys", nil, msgs)
}

func TestContextCompacted_NotifyFalse_NoEvent(t *testing.T) {
	bus := &captureBus{}
	cm := makeCompactingCM(bus, boolPtr(false))

	msgs := largeMessages(5, 50)
	cm.Manage(context.Background(), "sys", nil, msgs)

	if len(bus.events) != 0 {
		t.Errorf("Notify=false: expected no events, got %d", len(bus.events))
	}
}

// ---------------------------------------------------------------------------
// T6.2 — ContextManager integration tests
// ---------------------------------------------------------------------------

// summarizingMockProvider returns a canned summary response to any Chat call.
type summarizingMockProvider struct {
	cmMockProvider
	summaryText string
}

func (m *summarizingMockProvider) Chat(_ context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{Content: m.summaryText}, nil
}

// makeSmartCM builds a ContextManager with a summarizing mock provider.
// maxTokens must be small enough that the provided messages exceed the threshold.
func makeSmartCM(maxTokens int, threshold float64, protectedTurns int, summaryText string) *ContextManager {
	cfg := config.ContextConfig{
		MaxTokens:          maxTokens,
		CompactThreshold:   threshold,
		Strategy:           "smart",
		CooldownTurns:      0,
		ProtectedTurns:     protectedTurns,
		ToolResultMaxChars: 800,
		SummaryMaxTokens:   200,
		FallbackCtxSize:    maxTokens,
	}
	prov := &summarizingMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		summaryText:    summaryText,
	}
	return NewContextManager(cfg, prov, nil)
}

func TestContextManager_Integration_SmartCompaction_ReducesMessages(t *testing.T) {
	// (a) Smart strategy: exceed threshold → verify compaction (fewer messages or tokens after)
	cm := makeSmartCM(500, 0.1, 1, "Summary of earlier conversation.")

	// 10 large messages, each ~100 tokens → 1000 tokens >> 10% of 500 = 50 token threshold
	msgs := largeMessages(10, 100)
	tokensBefore := EstimateMessagesTokens(msgs)

	result := cm.Manage(context.Background(), "sys", nil, msgs)

	tokensAfter := EstimateMessagesTokens(result)
	if tokensAfter >= tokensBefore {
		t.Errorf("smart compaction: expected fewer tokens after compaction, got before=%d after=%d",
			tokensBefore, tokensAfter)
	}
}

func TestContextManager_Integration_SmartCompaction_PinnedSummaryExists(t *testing.T) {
	// (b) Pinned summary message starts with "[Context Summary" after compaction
	cm := makeSmartCM(500, 0.1, 1, "Summary of earlier conversation.")

	msgs := largeMessages(10, 100)
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	if len(result) == 0 {
		t.Fatal("expected non-empty result after compaction")
	}
	firstText := result[0].Content.TextOnly()
	if !strings.HasPrefix(firstText, "[Context Summary") {
		preview := firstText
		if len(preview) > 60 {
			preview = preview[:60]
		}
		t.Errorf("first message after compaction should start with '[Context Summary', got: %q", preview)
	}
}

func TestContextManager_Integration_SmartCompaction_SecondCompactionReplacesSummary(t *testing.T) {
	// (c) Second compaction replaces summary — only one summary at any time
	cm := makeSmartCM(500, 0.1, 1, "Summary of earlier conversation.")

	msgs := largeMessages(10, 100)

	// First compaction
	result1 := cm.Manage(context.Background(), "sys", nil, msgs)

	summaryCount := 0
	for _, m := range result1 {
		if strings.HasPrefix(m.Content.TextOnly(), "[Context Summary") {
			summaryCount++
		}
	}
	if summaryCount != 1 {
		t.Errorf("after first compaction: expected exactly 1 summary, got %d", summaryCount)
	}

	// Feed the compacted result + more large messages → trigger second compaction
	// Reset cooldown by advancing turn counter past cooldown window
	cm.lastCompactTurn = 0 // reset so next call can compact again

	moreMsgs := append(result1, largeMessages(5, 100)...)
	result2 := cm.Manage(context.Background(), "sys", nil, moreMsgs)

	summaryCount2 := 0
	for _, m := range result2 {
		if strings.HasPrefix(m.Content.TextOnly(), "[Context Summary") {
			summaryCount2++
		}
	}
	if summaryCount2 != 1 {
		t.Errorf("after second compaction: expected exactly 1 summary (old replaced), got %d", summaryCount2)
	}
}

func TestContextManager_Integration_LegacyStrategy_TruncatesByCount(t *testing.T) {
	// (d) Legacy strategy: legacyFn truncates by history length count
	cfg := config.ContextConfig{
		MaxTokens: 10000,
		Strategy:  "legacy",
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	histLen := 5
	// Wire a legacyFn that truncates to histLen
	cm.legacyFn = func(_ context.Context, messages []provider.ChatMessage) []provider.ChatMessage {
		if len(messages) > histLen {
			return messages[len(messages)-histLen:]
		}
		return messages
	}

	msgs := makeMessages(20) // 20 messages, well above histLen=5
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	if len(result) != histLen {
		t.Errorf("legacy strategy: expected %d messages after truncation, got %d", histLen, len(result))
	}
}

func TestContextManager_Integration_NoneStrategy_AlwaysUnchanged(t *testing.T) {
	// (e) None strategy: messages always returned unchanged regardless of size
	cfg := config.ContextConfig{
		MaxTokens: 100, // very small — would trigger compaction under smart
		Strategy:  "none",
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	cm := NewContextManager(cfg, prov, nil)

	msgs := largeMessages(20, 200) // huge messages — far exceed any threshold
	result := cm.Manage(context.Background(), "sys", nil, msgs)

	if len(result) != len(msgs) {
		t.Errorf("none strategy: expected %d unchanged messages, got %d", len(msgs), len(result))
	}
	for i, m := range result {
		if m.Content.TextOnly() != msgs[i].Content.TextOnly() {
			t.Errorf("none strategy: message %d content changed unexpectedly", i)
		}
	}
}

func TestContextCompacted_ForceCompact_EmitsEvent(t *testing.T) {
	bus := &captureBus{}
	cm := makeCompactingCM(bus, nil)

	msgs := largeMessages(5, 50)
	cm.ForceCompact(context.Background(), "sys", nil, msgs)

	if len(bus.events) == 0 {
		t.Fatal("ForceCompact: expected event to be emitted, got none")
	}

	ev := bus.events[0]
	if ev.Type != notify.EventContextCompacted {
		t.Errorf("ForceCompact event type = %q, want %q", ev.Type, notify.EventContextCompacted)
	}
	if ev.Text == "" {
		t.Error("ForceCompact event Text should not be empty")
	}
	if _, ok := ev.Meta["tokens_before"]; !ok {
		t.Error("ForceCompact event Meta missing 'tokens_before'")
	}
	if _, ok := ev.Meta["tokens_after"]; !ok {
		t.Error("ForceCompact event Meta missing 'tokens_after'")
	}
	if _, ok := ev.Meta["turns_summarized"]; !ok {
		t.Error("ForceCompact event Meta missing 'turns_summarized'")
	}
}
