package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/skill"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// capturedReply — captures reply strings from command handlers
// ---------------------------------------------------------------------------

type capturedReply struct {
	messages []string
}

func (c *capturedReply) reply(text string) {
	c.messages = append(c.messages, text)
}

// ---------------------------------------------------------------------------
// CommandRegistry tests
// ---------------------------------------------------------------------------

func TestCommandRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewCommandRegistry()
	var called bool
	reg.Register("foo", "does foo", func(cc CommandContext) error {
		called = true
		return nil
	})

	h, ok := reg.Lookup("foo")
	if !ok {
		t.Fatal("expected to find 'foo', got not found")
	}
	if err := h(CommandContext{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("handler was not called")
	}

	_, ok = reg.Lookup("bar")
	if ok {
		t.Error("expected 'bar' to not be found")
	}
}

func TestCommandRegistry_Entries(t *testing.T) {
	reg := NewCommandRegistry()
	reg.Register("alpha", "desc alpha", func(cc CommandContext) error { return nil })
	reg.Register("beta", "desc beta", func(cc CommandContext) error { return nil })
	reg.Register("gamma", "desc gamma", func(cc CommandContext) error { return nil })

	entries := reg.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries["alpha"] != "desc alpha" {
		t.Errorf("expected desc alpha for 'alpha', got %q", entries["alpha"])
	}
	if entries["beta"] != "desc beta" {
		t.Errorf("expected desc beta for 'beta', got %q", entries["beta"])
	}
	if entries["gamma"] != "desc gamma" {
		t.Errorf("expected desc gamma for 'gamma', got %q", entries["gamma"])
	}
}

func TestCommandRegistry_Names(t *testing.T) {
	reg := NewCommandRegistry()
	reg.Register("charlie", "c", func(cc CommandContext) error { return nil })
	reg.Register("alpha", "a", func(cc CommandContext) error { return nil })
	reg.Register("bravo", "b", func(cc CommandContext) error { return nil })

	names := reg.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d: %v", len(names), names)
	}
	// Names() must return sorted slice
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("expected sorted names [alpha bravo charlie], got %v", names)
	}
}

func TestCommandRegistry_DuplicateOverwrite(t *testing.T) {
	reg := NewCommandRegistry()

	var which string
	reg.Register("foo", "first", func(cc CommandContext) error {
		which = "first"
		return nil
	})
	reg.Register("foo", "second", func(cc CommandContext) error {
		which = "second"
		return nil
	})

	h, ok := reg.Lookup("foo")
	if !ok {
		t.Fatal("expected to find 'foo' after double register")
	}
	if err := h(CommandContext{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if which != "second" {
		t.Errorf("expected second handler to win, got %q", which)
	}
}

// ---------------------------------------------------------------------------
// parseCommand tests
// ---------------------------------------------------------------------------

func TestParseCommand_ValidCommands(t *testing.T) {
	cases := []struct {
		input    string
		wantName string
		wantArgs string
	}{
		{"/help", "help", ""},
		{"/reset", "reset", ""},
		{"/cancel 123", "cancel", "123"},
		{"/schedule * * * * * do thing", "schedule", "* * * * * do thing"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			name, args, ok := parseCommand(tc.input)
			if !ok {
				t.Fatalf("expected isCommand=true for %q", tc.input)
			}
			if name != tc.wantName {
				t.Errorf("name: want %q got %q", tc.wantName, name)
			}
			if args != tc.wantArgs {
				t.Errorf("args: want %q got %q", tc.wantArgs, args)
			}
		})
	}
}

func TestParseCommand_NotCommands(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"/path/to/file",
		"/ ",
		"/123",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			name, args, ok := parseCommand(tc)
			if ok {
				t.Errorf("expected isCommand=false for %q, got name=%q args=%q", tc, name, args)
			}
		})
	}
}

func TestParseCommand_EdgeCases(t *testing.T) {
	// Uppercase is lowercased
	name, args, ok := parseCommand("/HELP")
	if !ok {
		t.Fatal("expected isCommand=true for /HELP")
	}
	if name != "help" {
		t.Errorf("expected 'help', got %q", name)
	}
	if args != "" {
		t.Errorf("expected empty args, got %q", args)
	}

	// Single char command
	name, _, ok = parseCommand("/a")
	if !ok {
		t.Fatal("expected isCommand=true for /a")
	}
	if name != "a" {
		t.Errorf("expected 'a', got %q", name)
	}

	// Underscores in name
	name, _, ok = parseCommand("/cmd_with_underscores")
	if !ok {
		t.Fatal("expected isCommand=true for /cmd_with_underscores")
	}
	if name != "cmd_with_underscores" {
		t.Errorf("expected 'cmd_with_underscores', got %q", name)
	}
}

// ---------------------------------------------------------------------------
// Built-in command handler tests
// ---------------------------------------------------------------------------

func makeTestCC(cr *capturedReply, st store.Store, inbox chan<- channel.IncomingMessage) CommandContext {
	reg := NewCommandRegistry()
	registerBuiltinCommands(reg)
	return CommandContext{
		Ctx:          context.Background(),
		ChannelID:    "chan:42",
		SenderID:     "user:7",
		Args:         "",
		Store:        st,
		Config:       &config.AgentConfig{},
		Reply:        cr.reply,
		Registry:     reg,
		ProviderName: "mock-provider",
		ChannelName:  "test-channel",
		StartedAt:    time.Now().Add(-5 * time.Second),
		Inbox:        inbox,
	}
}

func TestCmdPing(t *testing.T) {
	cr := &capturedReply{}
	cc := makeTestCC(cr, &mockStore{}, nil)
	if err := cmdPing(cc); err != nil {
		t.Fatalf("cmdPing returned error: %v", err)
	}
	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "pong") {
		t.Errorf("expected reply to contain 'pong', got %q", cr.messages[0])
	}
}

func TestCmdHelp(t *testing.T) {
	cr := &capturedReply{}
	reg := NewCommandRegistry()
	reg.Register("alpha", "first command", func(cc CommandContext) error { return nil })
	reg.Register("beta", "second command", func(cc CommandContext) error { return nil })
	reg.Register("gamma", "third command", func(cc CommandContext) error { return nil })

	cc := CommandContext{
		Ctx:      context.Background(),
		Reply:    cr.reply,
		Registry: reg,
	}
	if err := cmdHelp(cc); err != nil {
		t.Fatalf("cmdHelp returned error: %v", err)
	}
	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(reply, name) {
			t.Errorf("expected reply to contain %q, got:\n%s", name, reply)
		}
	}
}

func TestCmdReset(t *testing.T) {
	st := &mockStore{}
	cr := &capturedReply{}
	cc := makeTestCC(cr, st, nil)
	if err := cmdReset(cc); err != nil {
		t.Fatalf("cmdReset returned error: %v", err)
	}
	if st.conv == nil {
		t.Fatal("expected SaveConversation to have been called")
	}
	if len(st.conv.Messages) != 0 {
		t.Errorf("expected empty messages after reset, got %d", len(st.conv.Messages))
	}
}

func TestCmdStatus(t *testing.T) {
	cr := &capturedReply{}
	cc := makeTestCC(cr, &mockStore{}, nil)
	if err := cmdStatus(cc); err != nil {
		t.Fatalf("cmdStatus returned error: %v", err)
	}
	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]
	if !strings.Contains(reply, "mock-provider") {
		t.Errorf("expected provider name in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "test-channel") {
		t.Errorf("expected channel name in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "Uptime") {
		t.Errorf("expected 'Uptime' in reply, got:\n%s", reply)
	}
}

func TestCmdWhoami(t *testing.T) {
	cr := &capturedReply{}
	cc := makeTestCC(cr, &mockStore{}, nil)
	if err := cmdWhoami(cc); err != nil {
		t.Fatalf("cmdWhoami returned error: %v", err)
	}
	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	reply := cr.messages[0]
	if !strings.Contains(reply, "user:7") {
		t.Errorf("expected sender ID 'user:7' in reply, got:\n%s", reply)
	}
	if !strings.Contains(reply, "chan:42") {
		t.Errorf("expected channel ID 'chan:42' in reply, got:\n%s", reply)
	}
}

func TestCmdRetry_WithHistory(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock("first message")},
		{Role: "assistant", Content: content.TextBlock("first reply")},
		{Role: "user", Content: content.TextBlock("retry this")},
	}
	st := &mockStore{
		conv: &store.Conversation{
			ID:        "conv_chan:42:user:7",
			ChannelID: "chan:42",
			Messages:  msgs,
		},
	}

	inbox := make(chan channel.IncomingMessage, 1)
	cr := &capturedReply{}
	cc := makeTestCC(cr, st, inbox)

	if err := cmdRetry(cc); err != nil {
		t.Fatalf("cmdRetry returned error: %v", err)
	}

	// Conversation should be trimmed: remove last user turn → 2 messages
	if st.conv == nil {
		t.Fatal("expected SaveConversation to have been called")
	}
	if len(st.conv.Messages) != 2 {
		t.Errorf("expected 2 messages after retry trim, got %d", len(st.conv.Messages))
	}

	// Synthetic message should be in the inbox
	select {
	case synthetic := <-inbox:
		if !strings.Contains(synthetic.Content.TextOnly(), "retry this") {
			t.Errorf("expected synthetic message to contain 'retry this', got %q", synthetic.Content.TextOnly())
		}
	default:
		t.Error("expected synthetic message in inbox, got none")
	}
}

func TestCmdRetry_NoHistory(t *testing.T) {
	st := &mockStore{
		conv: &store.Conversation{
			ID:        "conv_chan:42:user:7",
			ChannelID: "chan:42",
			Messages:  nil,
		},
	}

	inbox := make(chan channel.IncomingMessage, 1)
	cr := &capturedReply{}
	cc := makeTestCC(cr, st, inbox)

	if err := cmdRetry(cc); err != nil {
		t.Fatalf("cmdRetry returned error: %v", err)
	}
	if len(cr.messages) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(cr.messages))
	}
	if !strings.Contains(cr.messages[0], "Nothing to retry") {
		t.Errorf("expected 'Nothing to retry' reply, got %q", cr.messages[0])
	}

	// Inbox should be empty
	select {
	case <-inbox:
		t.Error("expected inbox to be empty for no-history retry")
	default:
	}
}

// ---------------------------------------------------------------------------
// Integration tests: slash command dispatch vs LLM
// ---------------------------------------------------------------------------

func TestSlashCommand_Integration_HelpNoLLM(t *testing.T) {
	prov := &mockProvider{}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "chan:1",
		SenderID:  "user:1",
		Content:   content.TextBlock("/help"),
	})

	if prov.callCount() != 0 {
		t.Errorf("expected no LLM calls for /help, got %d", prov.callCount())
	}

	sent := ch.sentMessages()
	if len(sent) == 0 {
		t.Fatal("expected at least one reply message for /help")
	}
	// Should contain built-in command names
	combined := ""
	for _, m := range sent {
		combined += m.Text
	}
	if !strings.Contains(combined, "ping") || !strings.Contains(combined, "help") {
		t.Errorf("expected help reply to list commands (ping, help), got:\n%s", combined)
	}
}

func TestSlashCommand_Integration_RegularMessageHitsLLM(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "hello back"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "chan:1",
		SenderID:  "user:1",
		Content:   content.TextBlock("hello"),
	})

	if prov.callCount() == 0 {
		t.Error("expected at least one LLM call for regular message")
	}
}
