package agent

// T4.2 tests: ContextManager wiring into Agent.New()
// T4.3 tests: contextMgr.Manage() integration in the loop
// T4.4 tests: /compact command

import (
	"context"
	"strings"
	"testing"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/skill"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// T4.2 — New() creates non-nil contextMgr and synthesizes strategy from legacy
// ---------------------------------------------------------------------------

func TestNew_ContextMgr_NonNil(t *testing.T) {
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxContextTokens: 50000,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr after New()")
	}
}

func TestNew_ContextMgr_Synthesis_SmartFromMaxContextTokens(t *testing.T) {
	// MaxContextTokens > 0 → strategy "smart"
	cfg := config.AgentConfig{
		MaxContextTokens: 80000,
		SummaryTokens:    1500,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "smart" {
		t.Errorf("expected strategy 'smart', got %q", ag.contextMgr.cfg.Strategy)
	}
}

func TestNew_ContextMgr_Synthesis_LegacyFromHistoryLength(t *testing.T) {
	// HistoryLength > 0, MaxContextTokens == 0 → strategy "legacy"
	cfg := config.AgentConfig{
		HistoryLength:    20,
		MaxContextTokens: 0,
	}
	// Use zero-value Context so synthesis kicks in
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "legacy" {
		t.Errorf("expected strategy 'legacy', got %q", ag.contextMgr.cfg.Strategy)
	}
}

func TestNew_ContextMgr_Synthesis_NoneWhenNeitherSet(t *testing.T) {
	// MaxContextTokens == 0 and HistoryLength == 0 → strategy "none"
	cfg := config.AgentConfig{
		MaxContextTokens: 0,
		HistoryLength:    0,
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "none" {
		t.Errorf("expected strategy 'none', got %q", ag.contextMgr.cfg.Strategy)
	}
}

// ---------------------------------------------------------------------------
// T4.3 — contextMgr.Manage() is called and modifies messages when needed
// ---------------------------------------------------------------------------

// trackingContextManager wraps ContextManager and tracks calls to Manage.
//
//nolint:unused // kept for future integration test assertions
type trackingContextManager struct {
	*ContextManager
	manageCalled int
	lastInputLen int
}

//nolint:unused // kept for future integration test assertions
func (t *trackingContextManager) Manage(
	ctx context.Context,
	systemPrompt string,
	toolDefs []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	t.manageCalled++
	t.lastInputLen = len(messages)
	return t.ContextManager.Manage(ctx, systemPrompt, toolDefs, messages)
}

func TestLoop_ContextMgr_Manage_CalledPerTurn(t *testing.T) {
	// Verify contextMgr.Manage is invoked during processMessage.
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		// Use "none" strategy via Context field to avoid actual compaction.
		Context: config.ContextConfig{
			Strategy:  "none",
			MaxTokens: 10000,
		},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "hello"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// The contextMgr should be non-nil after New with Context set.
	if ag.contextMgr == nil {
		t.Fatal("contextMgr is nil")
	}

	// With strategy "none", Manage is a no-op but still gets called.
	// We verify it via the currentTurn counter which increments on each Manage call.
	initialTurn := ag.contextMgr.currentTurn

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("hello"),
	})

	if ag.contextMgr.currentTurn <= initialTurn {
		t.Errorf("expected currentTurn to increment after processMessage, got %d (was %d)",
			ag.contextMgr.currentTurn, initialTurn)
	}
}

func TestLoop_ContextMgr_LegacyConfig_DoesNotCrash(t *testing.T) {
	// strategy "legacy" path should not crash — legacyManage stub returns unchanged.
	cfg := config.AgentConfig{
		MaxIterations:    1,
		MaxTokensPerTurn: 100,
		Context: config.ContextConfig{
			Strategy:  "legacy",
			MaxTokens: 10000,
		},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "done"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// processMessage should not panic or crash.
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("test message"),
	})

	if len(ch.sent) == 0 {
		t.Error("expected at least one reply, got none")
	}
}

// ---------------------------------------------------------------------------
// T4.4 — /compact command
// ---------------------------------------------------------------------------

func TestCompactCommand_EmptyConversation(t *testing.T) {
	// /compact with empty conversation → "Nothing to compact"
	cfg := config.AgentConfig{
		Context: config.ContextConfig{
			Strategy:  "smart",
			MaxTokens: 10000,
		},
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{} // no saved conversation → ErrNotFound → empty

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	var replied string
	cc := CommandContext{
		Ctx:       context.Background(),
		ChannelID: "test-channel",
		SenderID:  "user1",
		Store:     st,
		Config:    &ag.config,
		Reply: func(s string) {
			replied = s
		},
	}

	// Call cmdCompact directly (it's registered in the commands registry,
	// but we call it directly here to test the handler logic).
	_ = ag.cmdCompact(cc)

	if !strings.Contains(replied, "Nothing to compact") {
		t.Errorf("expected 'Nothing to compact', got %q", replied)
	}
}

func TestCompactCommand_WithMessages_CallsForceCompact(t *testing.T) {
	// /compact with messages → reports token counts
	cfg := config.AgentConfig{
		Context: config.ContextConfig{
			Strategy:  "smart",
			MaxTokens: 10000,
		},
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}

	// Pre-seed a conversation with messages.
	convID := "conv_test-channel:user1"
	existingConv := &store.Conversation{
		ID:        convID,
		ChannelID: "test-channel",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock("hello world how are you doing today?")},
			{Role: "assistant", Content: content.TextBlock("I'm doing great! How can I help you?")},
			{Role: "user", Content: content.TextBlock("can you help me with some code?")},
		},
	}
	st := &mockStore{conv: existingConv}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	var replied string
	cc := CommandContext{
		Ctx:       context.Background(),
		ChannelID: "test-channel",
		SenderID:  "user1",
		Store:     st,
		Config:    &ag.config,
		Reply: func(s string) {
			replied = s
		},
	}

	_ = ag.cmdCompact(cc)

	// Should report compaction (format: "Compacted: N → M tokens" or similar)
	if !strings.Contains(replied, "Compacted") {
		t.Errorf("expected 'Compacted' in reply, got %q", replied)
	}
}

// ---------------------------------------------------------------------------
// T6.3 — Backward compat smoke tests
// ---------------------------------------------------------------------------

func TestNew_LegacyFlatFields_OnlyMaxContextTokens_SmartStrategy(t *testing.T) {
	// Agent created with only legacy flat MaxContextTokens (no Context block)
	// → contextMgr created with strategy "smart" and correct MaxTokens wired.
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxContextTokens: 75000,
		SummaryTokens:    1200,
		// Context field is zero-value (Strategy == "")
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "smart" {
		t.Errorf("expected strategy 'smart' from MaxContextTokens, got %q", ag.contextMgr.cfg.Strategy)
	}
	// MaxTokens should be wired from MaxContextTokens
	if ag.contextMgr.cfg.ResolveMaxTokens() != 75000 {
		t.Errorf("expected ResolveMaxTokens() = 75000, got %d", ag.contextMgr.cfg.ResolveMaxTokens())
	}
	// SummaryMaxTokens should be inherited from SummaryTokens
	if ag.contextMgr.cfg.SummaryMaxTokens != 1200 {
		t.Errorf("expected SummaryMaxTokens = 1200, got %d", ag.contextMgr.cfg.SummaryMaxTokens)
	}
}

func TestNew_LegacyFlatFields_OnlyHistoryLength_LegacyStrategy(t *testing.T) {
	// Agent created with only legacy flat HistoryLength (no MaxContextTokens, no Context block)
	// → contextMgr created with strategy "legacy" and legacyFn wired.
	cfg := config.AgentConfig{
		MaxIterations: 5,
		HistoryLength: 15,
		// MaxContextTokens == 0, Context field zero-value
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "legacy" {
		t.Errorf("expected strategy 'legacy' from HistoryLength, got %q", ag.contextMgr.cfg.Strategy)
	}
	// legacyFn must be wired for legacy strategy
	if ag.contextMgr.legacyFn == nil {
		t.Error("expected legacyFn to be wired for legacy strategy")
	}
	// legacyFn should truncate messages over HistoryLength.
	// legacyTruncate returns histLen tail messages + optional summary (+ optional preserved first user msg),
	// so the result count is at most histLen+2 and must be strictly less than the input count.
	msgs := makeMessages(30) // 30 messages > 15 historyLen
	result := ag.contextMgr.legacyFn(context.Background(), msgs)
	if len(result) >= len(msgs) {
		t.Errorf("legacyFn: expected fewer messages after truncation (got %d, input was %d)", len(result), len(msgs))
	}
}

func TestNew_LegacyFlatFields_NeitherSet_NoneStrategy(t *testing.T) {
	// Agent with MaxContextTokens == 0 and HistoryLength == 0 → strategy "none"
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxContextTokens: 0,
		HistoryLength:    0,
		// Context field zero-value
	}
	prov := &cmMockProvider{name: "test", model: "test-model"}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.contextMgr == nil {
		t.Fatal("expected non-nil contextMgr")
	}
	if ag.contextMgr.cfg.Strategy != "none" {
		t.Errorf("expected strategy 'none' when neither field set, got %q", ag.contextMgr.cfg.Strategy)
	}
	// None strategy: Manage returns messages unchanged
	msgs := makeMessages(5)
	result := ag.contextMgr.Manage(context.Background(), "sys", nil, msgs)
	if len(result) != len(msgs) {
		t.Errorf("none strategy: expected %d unchanged messages, got %d", len(msgs), len(result))
	}
}
