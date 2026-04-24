package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/skill"
	"daimon/internal/store"
	"daimon/internal/tool"
)

// --- C2. shouldGenerateTitle / firstUserMessageText ---

func TestShouldGenerateTitle(t *testing.T) {
	mkMsg := func(role, text string) provider.ChatMessage {
		return provider.ChatMessage{
			Role:    role,
			Content: content.Blocks{{Type: content.BlockText, Text: text}},
		}
	}

	longEnough := strings.Repeat("a", 25) // 25 runes > 20
	sixMsgs := func(firstText string) []provider.ChatMessage {
		return []provider.ChatMessage{
			mkMsg("user", firstText),
			mkMsg("assistant", "x"),
			mkMsg("user", "y"),
			mkMsg("assistant", "z"),
			mkMsg("user", "w"),
			mkMsg("assistant", "v"),
		}
	}

	cases := []struct {
		name string
		conv *store.Conversation
		want bool
	}{
		{"eligible", &store.Conversation{Messages: sixMsgs(longEnough)}, true},
		{"first user too short", &store.Conversation{Messages: sixMsgs("hola")}, false},
		{
			"already has title",
			&store.Conversation{Messages: sixMsgs(longEnough), Metadata: map[string]string{"title": "Already Titled"}},
			false,
		},
		{"fewer than 6 messages", &store.Conversation{Messages: sixMsgs(longEnough)[:4]}, false},
		{"nil conv", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldGenerateTitle(tc.conv)
			if got != tc.want {
				t.Errorf("shouldGenerateTitle: got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- C3. Post-save title hook wiring ---

type fakeTitler struct {
	mu       sync.Mutex
	enqueues []string
}

func (f *fakeTitler) Enqueue(_ context.Context, convID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueues = append(f.enqueues, convID)
}

func (f *fakeTitler) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.enqueues))
	copy(out, f.enqueues)
	return out
}

// buildAgentForTitleHook constructs a minimal Agent whose processMessage runs
// through a single-response provider and saves a conv with enough messages to
// trigger shouldGenerateTitle. The preloaded conv already has 5 messages
// (3 user + 2 assistant); the incoming user message brings it to 6.
func buildAgentForTitleHook(t *testing.T, enabled bool, titler Titler, preloadedConv *store.Conversation) (*Agent, *mockStore) {
	t.Helper()
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "final response"}}}
	ch := &mockChannel{}
	st := &mockStore{conv: preloadedConv}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, map[string]tool.Tool{}, nil, skill.SkillIndex{}, 4, false)
	ag.WithAIConfig(config.AIConfig{
		TitleGeneration: config.TitleGenYAMLConfig{Enabled: enabled},
	})
	if titler != nil {
		ag.WithTitler(titler)
	}
	return ag, st
}

// preloadedEligibleConv has 5 messages; adding the user message = 6,
// making the conv eligible once processMessage saves.
func preloadedEligibleConv() *store.Conversation {
	mk := func(role, text string) provider.ChatMessage {
		return provider.ChatMessage{
			Role:    role,
			Content: content.Blocks{{Type: content.BlockText, Text: text}},
		}
	}
	return &store.Conversation{
		ID:        "conv_test:u1",
		ChannelID: "test",
		Messages: []provider.ChatMessage{
			mk("user", strings.Repeat("q", 25)), // first user >20 runes
			mk("assistant", "a"),
			mk("user", "b"),
			mk("assistant", "c"),
			mk("user", "d"),
		},
	}
}

func TestPostSaveTitleHook_EligibleConvTriggersEnqueue(t *testing.T) {
	titler := &fakeTitler{}
	ag, _ := buildAgentForTitleHook(t, true, titler, preloadedEligibleConv())

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		SenderID:  "u1",
		Content:   content.TextBlock("follow-up"),
	})

	calls := titler.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 Enqueue call, got %d: %v", len(calls), calls)
	}
}

func TestPostSaveTitleHook_ShortFirstMessageDoesNotTrigger(t *testing.T) {
	// Preload with first user msg "hola" (4 runes) — below threshold.
	mk := func(role, text string) provider.ChatMessage {
		return provider.ChatMessage{
			Role:    role,
			Content: content.Blocks{{Type: content.BlockText, Text: text}},
		}
	}
	conv := &store.Conversation{
		ID: "conv_test:u1", ChannelID: "test",
		Messages: []provider.ChatMessage{
			mk("user", "hola"),
			mk("assistant", "hi"),
			mk("user", "ok"),
			mk("assistant", "sure"),
			mk("user", "thx"),
		},
	}

	titler := &fakeTitler{}
	ag, _ := buildAgentForTitleHook(t, true, titler, conv)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test", SenderID: "u1",
		Content: content.TextBlock("bye"),
	})

	if n := len(titler.Calls()); n != 0 {
		t.Errorf("expected 0 Enqueue calls, got %d", n)
	}
}

func TestPostSaveTitleHook_ExistingTitleIsNotOverwritten(t *testing.T) {
	conv := preloadedEligibleConv()
	conv.Metadata = map[string]string{"title": "manual title"}

	titler := &fakeTitler{}
	ag, _ := buildAgentForTitleHook(t, true, titler, conv)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test", SenderID: "u1",
		Content: content.TextBlock("follow-up"),
	})

	if n := len(titler.Calls()); n != 0 {
		t.Errorf("expected 0 Enqueue calls when title exists, got %d", n)
	}
}

func TestPostSaveTitleHook_DisabledViaConfig(t *testing.T) {
	titler := &fakeTitler{}
	ag, _ := buildAgentForTitleHook(t, false /* enabled */, titler, preloadedEligibleConv())

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test", SenderID: "u1",
		Content: content.TextBlock("follow-up"),
	})

	if n := len(titler.Calls()); n != 0 {
		t.Errorf("expected 0 Enqueue calls when disabled, got %d", n)
	}
}

func TestPostSaveTitleHook_NilTitlerIsNoOp(t *testing.T) {
	ag, _ := buildAgentForTitleHook(t, true, nil, preloadedEligibleConv())

	// Must not panic.
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test", SenderID: "u1",
		Content: content.TextBlock("follow-up"),
	})
}

// --- C1. convID resolution ---

func TestProcessMessage_ExplicitConversationIDIsRespected(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	ch := &mockChannel{}
	st := &mockStore{} // empty — LoadConversation returns ErrNotFound

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, map[string]tool.Tool{}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ConversationID: "conv_web:custom:u42",
		ChannelID:      "ignored-channel",
		SenderID:       "ignored-sender",
		Content:        content.TextBlock("hello"),
	})

	if st.conv == nil {
		t.Fatal("expected conv to be saved")
	}
	if st.conv.ID != "conv_web:custom:u42" {
		t.Errorf("convID: got %q, want conv_web:custom:u42 (userScope must be bypassed)", st.conv.ID)
	}
}

func TestProcessMessage_MissingConversationIDFallsBackToUserScope(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, map[string]tool.Tool{}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "cli",
		SenderID:  "alice",
		Content:   content.TextBlock("hi"),
	})

	if st.conv == nil || st.conv.ID != "conv_cli:alice" {
		t.Errorf("convID: got %v, want conv_cli:alice (userScope fallback)", st.conv)
	}
}

func TestProcessMessage_ExplicitConvIDCreatesFreshConvWhenMissing(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
	ch := &mockChannel{}
	st := &mockStore{} // no preload

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st,
		audit.NoopAuditor{}, map[string]tool.Tool{}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ConversationID: "conv_web:brand-new:u1",
		ChannelID:      "web:brand-new",
		SenderID:       "u1",
		Content:        content.TextBlock("first message"),
	})

	if st.conv == nil || st.conv.ID != "conv_web:brand-new:u1" {
		t.Errorf("fresh conv not created with explicit ID; got %v", st.conv)
	}
	// Exactly 2 messages: the user's input + the assistant's response.
	if len(st.conv.Messages) != 2 {
		t.Errorf("expected 2 messages (user + assistant), got %d", len(st.conv.Messages))
	}
}

// Silence "imported and not used" if other symbols stay unused in future edits.
var _ = json.Marshal
