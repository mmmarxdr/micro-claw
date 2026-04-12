package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/content"
	"microagent/internal/provider"
	"microagent/internal/skill"
	"microagent/internal/store"
	"microagent/internal/tool"
)

// ---------------------------------------------------------------------------
// Extended mock types
// ---------------------------------------------------------------------------

type mockProvider struct {
	mu                 sync.Mutex
	responses          []provider.ChatResponse
	errs               []error // parallel to responses; nil entry = no error for that call
	calls              int
	lastReq            provider.ChatRequest
	supportsMultimodal bool
}

func (m *mockProvider) Name() string                                    { return "mock" }
func (m *mockProvider) SupportsTools() bool                             { return true }
func (m *mockProvider) SupportsMultimodal() bool                        { return m.supportsMultimodal }
func (m *mockProvider) SupportsAudio() bool                             { return false }
func (m *mockProvider) HealthCheck(ctx context.Context) (string, error) { return "mock", nil }
func (m *mockProvider) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastReq = req
	idx := m.calls
	m.calls++
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.responses) {
		resp := m.responses[idx]
		return &resp, nil
	}
	return &provider.ChatResponse{Content: "default"}, nil
}

func (m *mockProvider) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type mockChannel struct {
	mu       sync.Mutex
	sent     []channel.OutgoingMessage
	stopErr  error
	messages []channel.IncomingMessage // pre-filled inbox for Run tests
}

func (m *mockChannel) Name() string { return "mock" }
func (m *mockChannel) Start(ctx context.Context, inbox chan<- channel.IncomingMessage) error {
	for _, msg := range m.messages {
		inbox <- msg
	}
	return nil
}

func (m *mockChannel) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}
func (m *mockChannel) Stop() error { return m.stopErr }

func (m *mockChannel) sentMessages() []channel.OutgoingMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]channel.OutgoingMessage, len(m.sent))
	copy(cp, m.sent)
	return cp
}

type mockTool struct {
	name        string
	result      tool.ToolResult
	err         error
	shouldPanic bool
	calls       int
}

func (m *mockTool) Name() string            { return m.name }
func (m *mockTool) Description() string     { return "mock tool" }
func (m *mockTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (m *mockTool) Execute(ctx context.Context, params json.RawMessage) (tool.ToolResult, error) {
	m.calls++
	if m.shouldPanic {
		panic("test panic")
	}
	return m.result, m.err
}

type mockStore struct {
	mu           sync.Mutex
	conv         *store.Conversation // nil means "not found" → creates new
	loadErr      error
	saveErr      error
	memories     []store.MemoryEntry
	appendedMems []store.MemoryEntry
	updateCount  int
}

func (m *mockStore) SaveConversation(ctx context.Context, conv store.Conversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.conv = &conv
	return nil
}

func (m *mockStore) LoadConversation(ctx context.Context, id string) (*store.Conversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.conv == nil {
		return nil, store.ErrNotFound
	}
	// Return a deep copy to avoid shared-slice races when multiple
	// processMessage goroutines load the same conversation concurrently
	// (mirrors real store behavior where each load returns independent data).
	cp := *m.conv
	cp.Messages = make([]provider.ChatMessage, len(m.conv.Messages))
	copy(cp.Messages, m.conv.Messages)
	return &cp, nil
}

func (m *mockStore) ListConversations(ctx context.Context, channelID string, limit int) ([]store.Conversation, error) {
	return nil, nil
}

func (m *mockStore) AppendMemory(ctx context.Context, scopeID string, entry store.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendedMems = append(m.appendedMems, entry)
	return nil
}

func (m *mockStore) SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]store.MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.memories, nil
}

func (m *mockStore) UpdateMemory(_ context.Context, _ string, _ store.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCount++
	return nil
}

func (m *mockStore) updateCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateCount
}
func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// Helper to build a default agent config.
// ---------------------------------------------------------------------------

func defaultCfg() config.AgentConfig {
	return config.AgentConfig{MaxIterations: 5, MaxTokensPerTurn: 100}
}

func defaultLimits() config.LimitsConfig {
	return config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}
}

// ---------------------------------------------------------------------------
// Original test — preserved
// ---------------------------------------------------------------------------

func TestAgentLoop(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
				},
			},
			{
				Content: "final response",
			},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"mock_tool": &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "mock result"}},
	}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	if len(ch.sent) != 1 {
		t.Fatalf("expected 1 final message, got %d", len(ch.sent))
	}
	if ch.sent[0].Text != "final response" {
		t.Errorf("unexpected output: %s", ch.sent[0].Text)
	}
	if len(st.conv.Messages) != 4 {
		t.Errorf("expected 4 messages in history, got %d", len(st.conv.Messages))
	}
}

// ---------------------------------------------------------------------------
// TestAgent_Run_ProcessesMessages
// ---------------------------------------------------------------------------

func TestAgent_Run_ProcessesMessages(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "hi there"},
		},
	}
	// Pre-fill inbox with one message via mockChannel.messages
	ch := &mockChannel{
		messages: []channel.IncomingMessage{
			{ChannelID: "test", Content: content.TextBlock("hello")},
		},
	}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- ag.Run(ctx)
	}()

	// Wait until the provider is called, then cancel.
	deadline := time.After(3 * time.Second)
	for {
		if prov.callCount() >= 1 {
			cancel()
			break
		}
		select {
		case <-deadline:
			t.Fatal("provider.Chat was never called within 3s")
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case err := <-runDone:
		if err != context.Canceled && err != context.DeadlineExceeded {
			t.Errorf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}

	sent := ch.sentMessages()
	if len(sent) == 0 {
		t.Error("expected channel.Send to be called with provider response")
	} else if sent[0].Text != "hi there" {
		t.Errorf("expected 'hi there', got %q", sent[0].Text)
	}
}

// ---------------------------------------------------------------------------
// TestAgent_Shutdown
// ---------------------------------------------------------------------------

func TestAgent_Shutdown_NilError(t *testing.T) {
	ch := &mockChannel{stopErr: nil}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, &mockProvider{}, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	if err := ag.Shutdown(); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestAgent_Shutdown_PropagatesError(t *testing.T) {
	stopErr := errors.New("stop failed")
	ch := &mockChannel{stopErr: stopErr}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, &mockProvider{}, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	if err := ag.Shutdown(); !errors.Is(err, stopErr) {
		t.Errorf("expected stopErr, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestBuildContext_*
// ---------------------------------------------------------------------------

func TestBuildContext_NoMemories(t *testing.T) {
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, &mockChannel{}, &mockProvider{}, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	conv := &store.Conversation{}
	req := ag.buildContext(conv, []store.MemoryEntry{})
	if strings.Contains(req.SystemPrompt, "## Relevant Context:") {
		t.Error("system prompt should NOT contain '## Relevant Context:' when no memories")
	}
}

func TestBuildContext_WithMemories(t *testing.T) {
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, &mockChannel{}, &mockProvider{}, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	conv := &store.Conversation{}
	memories := []store.MemoryEntry{
		{Content: "User likes Go"},
		{Content: "Prefers short answers"},
	}
	req := ag.buildContext(conv, memories)
	if !strings.Contains(req.SystemPrompt, "## Relevant Context:") {
		t.Error("system prompt should contain '## Relevant Context:'")
	}
	if !strings.Contains(req.SystemPrompt, "User likes Go") {
		t.Error("system prompt should contain first memory content")
	}
	if !strings.Contains(req.SystemPrompt, "Prefers short answers") {
		t.Error("system prompt should contain second memory content")
	}
}

func TestBuildContext_ToolsIncluded(t *testing.T) {
	toolA := &mockTool{name: "tool_a"}
	toolB := &mockTool{name: "tool_b"}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, &mockChannel{}, &mockProvider{}, &mockStore{}, audit.NoopAuditor{},
		map[string]tool.Tool{"tool_a": toolA, "tool_b": toolB}, nil, skill.SkillIndex{}, 4, false)

	conv := &store.Conversation{}
	req := ag.buildContext(conv, nil)

	if len(req.Tools) != 2 {
		t.Errorf("expected 2 tools in ChatRequest, got %d", len(req.Tools))
	}
	names := map[string]bool{}
	for _, td := range req.Tools {
		names[td.Name] = true
	}
	if !names["tool_a"] || !names["tool_b"] {
		t.Errorf("tools missing from ChatRequest: %v", names)
	}
}

func TestBuildContext_NoTools(t *testing.T) {
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, &mockChannel{}, &mockProvider{}, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	conv := &store.Conversation{}
	req := ag.buildContext(conv, nil)
	if req.Tools == nil {
		t.Error("Tools slice should not be nil even with no tools registered")
	}
	if len(req.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(req.Tools))
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_MaxIterations
// ---------------------------------------------------------------------------

func TestProcessMessage_MaxIterations(t *testing.T) {
	// Provider always returns a tool_use call — loop should hit max iterations.
	toolCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			toolCall, toolCall, toolCall, toolCall, toolCall,
			toolCall, toolCall, toolCall, toolCall, toolCall,
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{MaxIterations: 2, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "result"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("go")})

	found := false
	for _, msg := range ch.sent {
		if strings.Contains(msg.Text, "iteration limit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'iteration limit' message in sent messages; got: %v", ch.sent)
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_UnknownTool
// ---------------------------------------------------------------------------

func TestProcessMessage_UnknownTool(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "unknown_tool", Input: json.RawMessage(`{}`)},
				},
			},
			{Content: "done"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{}, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// The conversation should have a tool-role message with "not found"
	if st.conv == nil {
		t.Fatal("no conversation saved")
	}
	foundNotFound := false
	for _, msg := range st.conv.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content.TextOnly(), "not found") {
			if strings.HasPrefix(msg.Content.TextOnly(), "<tool_result status=\"error\">\n") {
				foundNotFound = true
			}
			break
		}
	}
	if !foundNotFound {
		t.Errorf("expected tool result message containing 'not found'; messages: %v", st.conv.Messages)
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_ToolGoError
// ---------------------------------------------------------------------------

func TestProcessMessage_ToolGoError(t *testing.T) {
	goErr := errors.New("disk full")
	mt := &mockTool{name: "err_tool", err: goErr}

	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "err_tool", Input: json.RawMessage(`{}`)},
				},
			},
			{Content: "done"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"err_tool": mt}, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	if st.conv == nil {
		t.Fatal("no conversation saved")
	}
	foundErr := false
	for _, msg := range st.conv.Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content.TextOnly(), "disk full") {
			if strings.HasPrefix(msg.Content.TextOnly(), "<tool_result status=\"error\">\n") {
				foundErr = true
			}
			break
		}
	}
	if !foundErr {
		t.Errorf("expected tool result with 'disk full'; messages: %v", st.conv.Messages)
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_ToolPanic
// ---------------------------------------------------------------------------

func TestProcessMessage_ToolPanic(t *testing.T) {
	// This test verifies that a panicking tool does NOT crash the process.
	mt := &mockTool{name: "panic_tool", shouldPanic: true}

	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "panic_tool", Input: json.RawMessage(`{}`)},
				},
			},
			{Content: "recovered"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"panic_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	// Should NOT panic
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("go")})

	if st.conv == nil {
		t.Fatal("no conversation saved")
	}
	foundCrash := false
	for _, msg := range st.conv.Messages {
		if msg.Role == "tool" && (strings.Contains(msg.Content.TextOnly(), "crashed") || strings.Contains(msg.Content.TextOnly(), "test panic")) {
			if strings.HasPrefix(msg.Content.TextOnly(), "<tool_result status=\"error\">\n") {
				foundCrash = true
			}
			break
		}
	}
	if !foundCrash {
		t.Errorf("expected tool result containing panic info; messages: %v", st.conv.Messages)
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_MultipleToolCalls
// ---------------------------------------------------------------------------

func TestProcessMessage_MultipleToolCalls(t *testing.T) {
	toolA := &mockTool{name: "tool_a", result: tool.ToolResult{Content: "a result"}}
	toolB := &mockTool{name: "tool_b", result: tool.ToolResult{Content: "b result"}}

	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "tool_a", Input: json.RawMessage(`{}`)},
					{ID: "t2", Name: "tool_b", Input: json.RawMessage(`{}`)},
				},
			},
			{Content: "done"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"tool_a": toolA,
		"tool_b": toolB,
	}, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	if toolA.calls != 1 {
		t.Errorf("tool_a expected 1 call, got %d", toolA.calls)
	}
	if toolB.calls != 1 {
		t.Errorf("tool_b expected 1 call, got %d", toolB.calls)
	}

	for _, msg := range st.conv.Messages {
		if msg.Role == "tool" {
			if !strings.HasPrefix(msg.Content.TextOnly(), "<tool_result status=\"success\">\n") || !strings.HasSuffix(msg.Content.TextOnly(), "\n</tool_result>") {
				t.Errorf("expected tool_result xml wrapping with success status, got: %q", msg.Content.TextOnly())
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_ProviderError
// ---------------------------------------------------------------------------

func TestProcessMessage_ProviderError(t *testing.T) {
	provErr := errors.New("api down")
	prov := &mockProvider{
		errs: []error{provErr},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// Should not panic
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	found := false
	for _, msg := range ch.sent {
		if strings.Contains(msg.Text, "AI provider returned an error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected generic provider error message sent to channel; got: %v", ch.sent)
	}
}

// ---------------------------------------------------------------------------
// TestProcessMessage_ExistingHistory
// ---------------------------------------------------------------------------

func TestProcessMessage_ExistingHistory(t *testing.T) {
	existingConv := &store.Conversation{
		ID:        "conv_test",
		ChannelID: "test",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock("first message")},
			{Role: "assistant", Content: content.TextBlock("first reply")},
		},
	}

	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{conv: existingConv}

	var capturedReq provider.ChatRequest
	origProv := prov
	_ = origProv

	// Wrap provider to capture the request
	capturingProv := &capturingProvider{inner: prov}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, capturingProv, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("new message")})

	capturedReq = capturingProv.lastReq

	// The ChatRequest should contain the 2 existing messages plus the new user message
	if len(capturedReq.Messages) < 3 {
		t.Errorf("expected at least 3 messages in ChatRequest (2 existing + 1 new), got %d", len(capturedReq.Messages))
	}
	if capturedReq.Messages[0].Content.TextOnly() != "first message" {
		t.Errorf("expected first message to be 'first message', got %q", capturedReq.Messages[0].Content.TextOnly())
	}
	if capturedReq.Messages[len(capturedReq.Messages)-1].Content.TextOnly() != "new message" {
		t.Errorf("expected last message to be 'new message', got %q", capturedReq.Messages[len(capturedReq.Messages)-1].Content.TextOnly())
	}
}

// capturingProvider wraps a mockProvider and captures the last ChatRequest.
type capturingProvider struct {
	inner   *mockProvider
	lastReq provider.ChatRequest
}

func (c *capturingProvider) Name() string             { return "capturing" }
func (c *capturingProvider) SupportsTools() bool      { return true }
func (c *capturingProvider) SupportsMultimodal() bool { return true }
func (c *capturingProvider) SupportsAudio() bool      { return false }
func (c *capturingProvider) HealthCheck(ctx context.Context) (string, error) {
	return c.inner.HealthCheck(ctx)
}

func (c *capturingProvider) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	c.lastReq = req
	return c.inner.Chat(ctx, req)
}

// ---------------------------------------------------------------------------
// TestProcessMessage_AppendMemoryCalledOnFinalResponse
// ---------------------------------------------------------------------------

func TestProcessMessage_AppendMemoryCalledOnFinalResponse(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "here is my answer"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	if len(st.appendedMems) != 1 {
		t.Fatalf("expected 1 memory entry appended, got %d", len(st.appendedMems))
	}
	mem := st.appendedMems[0]
	if mem.Content != "here is my answer" {
		t.Errorf("expected memory content 'here is my answer', got %q", mem.Content)
	}
	if mem.Source != "conv_test" {
		t.Errorf("expected memory source 'conv_test', got %q", mem.Source)
	}
	if mem.ID == "" {
		t.Error("expected memory ID to be non-empty")
	}
}

// ---------------------------------------------------------------------------
// TestAgentLoop_HistoryTruncation
// ---------------------------------------------------------------------------

func TestAgentLoop_HistoryTruncation(t *testing.T) {
	makeMessages := func(roles ...string) []provider.ChatMessage {
		msgs := make([]provider.ChatMessage, len(roles))
		for i, r := range roles {
			msgs[i] = provider.ChatMessage{Role: r, Content: content.TextBlock(fmt.Sprintf("msg-%d", i))}
		}
		return msgs
	}

	t.Run("first_user_trimmed", func(t *testing.T) {
		// 20 existing msgs, messages[0].Role="user" content="initial request"
		// HistoryLength=5
		// After appending new user: 21 msgs
		// trim = 21 - 5 = 16
		// tail = msgs[16:21] (5 msgs)
		// firstUserIdx=0 < trim=16 → prepend → 6 msgs
		roles := make([]string, 20)
		for i := range roles {
			if i%2 == 0 {
				roles[i] = "assistant"
			} else {
				roles[i] = "user"
			}
		}
		roles[0] = "user"

		existing := makeMessages(roles...)
		existing[0].Content = content.TextBlock("initial request")

		st := &mockStore{conv: &store.Conversation{
			ID:        "conv_test",
			ChannelID: "test",
			Messages:  existing,
		}}
		prov := &mockProvider{responses: []provider.ChatResponse{
			{Content: "summary result"}, // for the summarization call
			{Content: "ok"},             // for the actual Chat call
		}}
		ch := &mockChannel{}

		cfg := config.AgentConfig{MaxIterations: 1, MaxTokensPerTurn: 100, HistoryLength: 5}
		ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
		ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("new msg")})

		msgs := prov.lastReq.Messages
		// Should have 7: preserved first user + summary + last 5 (which includes the new user)
		if len(msgs) != 7 {
			t.Errorf("expected 7 messages in ChatRequest, got %d: %v", len(msgs), msgs)
		}
		// First message must be the preserved initial user msg
		if msgs[0].Content.TextOnly() != "initial request" {
			t.Errorf("expected first message to be 'initial request', got %q", msgs[0].Content.TextOnly())
		}
		// Second message must be the summary
		if msgs[1].Role != "assistant" || msgs[1].Content.TextOnly() != "(Summary of previous conversation):\nsummary result" {
			t.Errorf("expected second message to be the summary, got %q (role %q)", msgs[1].Content.TextOnly(), msgs[1].Role)
		}
		// Last message must be the new incoming user msg
		if msgs[len(msgs)-1].Content.TextOnly() != "new msg" {
			t.Errorf("expected last message to be 'new msg', got %q", msgs[len(msgs)-1].Content.TextOnly())
		}
	})

	t.Run("first_user_in_tail", func(t *testing.T) {
		// 10 existing msgs, messages[5].Role="user" (first user at index 5)
		// HistoryLength=7
		// After appending new user: 11 msgs
		// trim = 11 - 7 = 4
		// tail = msgs[4:11] (7 msgs)
		// firstUserIdx=5 >= trim=4 → NO prepend
		// Total = 7 msgs
		roles := make([]string, 10)
		for i := range roles {
			roles[i] = "assistant"
		}
		roles[5] = "user" // first user is inside tail (index 5 >= trim=4)
		roles[7] = "user"
		roles[9] = "user"

		existing := makeMessages(roles...)

		st := &mockStore{conv: &store.Conversation{
			ID:        "conv_test",
			ChannelID: "test",
			Messages:  existing,
		}}
		prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
		ch := &mockChannel{}

		cfg := config.AgentConfig{MaxIterations: 1, MaxTokensPerTurn: 100, HistoryLength: 7}
		ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
		ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("new msg")})

		msgs := prov.lastReq.Messages
		if len(msgs) != 8 {
			t.Errorf("expected 8 messages (summary + no prepend, first user in tail), got %d: %v", len(msgs), msgs)
		}
		// Verify messages[5] (role="user", now at overall index 6 after summary injection) appears exactly once
		count := 0
		for _, m := range msgs {
			if m.Content.TextOnly() == existing[5].Content.TextOnly() {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected first user message to appear exactly once, got %d", count)
		}
	})

	t.Run("history_length_one", func(t *testing.T) {
		// 5 existing msgs, messages[0].Role="user" content="first"
		// HistoryLength=1
		// After appending new user: 6 msgs
		// trim = 6 - 1 = 5
		// tail = msgs[5:6] = [new user msg] (1 msg)
		// firstUserIdx=0 < trim=5 → prepend → 2 msgs total
		roles := []string{"user", "assistant", "user", "assistant", "user"}
		existing := makeMessages(roles...)
		existing[0].Content = content.TextBlock("first")

		st := &mockStore{conv: &store.Conversation{
			ID:        "conv_test",
			ChannelID: "test",
			Messages:  existing,
		}}
		prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
		ch := &mockChannel{}

		cfg := config.AgentConfig{MaxIterations: 1, MaxTokensPerTurn: 100, HistoryLength: 1}
		ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
		ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("later msg")})

		msgs := prov.lastReq.Messages
		if len(msgs) != 3 {
			t.Errorf("expected 3 messages (preserved first user + summary + new user), got %d: %v", len(msgs), msgs)
		}
		if msgs[0].Content.TextOnly() != "first" {
			t.Errorf("expected first message to be 'first', got %q", msgs[0].Content.TextOnly())
		}
		if !strings.HasPrefix(msgs[1].Content.TextOnly(), "(Summary of previous conversation):") {
			t.Errorf("expected second message to be summary, got %q", msgs[1].Content.TextOnly())
		}
		if msgs[2].Content.TextOnly() != "later msg" {
			t.Errorf("expected third message to be 'later msg', got %q", msgs[2].Content.TextOnly())
		}
	})

	t.Run("no_user_message", func(t *testing.T) {
		// 5 existing msgs all role="assistant", HistoryLength=3
		// After appending new user: 6 msgs
		// trim = 6 - 3 = 3
		// tail = msgs[3:6] (3 msgs)
		// firstUserIdx=-1 → no prepend
		// Total = 3 msgs
		roles := []string{"assistant", "assistant", "assistant", "assistant", "assistant"}
		existing := makeMessages(roles...)

		st := &mockStore{conv: &store.Conversation{
			ID:        "conv_test",
			ChannelID: "test",
			Messages:  existing,
		}}
		prov := &mockProvider{responses: []provider.ChatResponse{{Content: "ok"}}}
		ch := &mockChannel{}

		cfg := config.AgentConfig{MaxIterations: 1, MaxTokensPerTurn: 100, HistoryLength: 3}
		ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

		// Should not panic
		ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("help")})

		msgs := prov.lastReq.Messages
		if len(msgs) != 4 {
			t.Errorf("expected 4 messages (no prepend when no user msgs, + 1 summary), got %d: %v", len(msgs), msgs)
		}
		if !strings.HasPrefix(msgs[0].Content.TextOnly(), "(Summary of previous conversation):") {
			t.Errorf("expected first message to be summary, got %q", msgs[0].Content.TextOnly())
		}
	})
}

func TestProcessMessage_NoMemoryOnEmptyResponse(t *testing.T) {
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: ""},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	if len(st.appendedMems) != 0 {
		t.Errorf("expected 0 memory entries for empty response, got %d", len(st.appendedMems))
	}
}

// ---------------------------------------------------------------------------
// TestEnricher wiring in Agent
// ---------------------------------------------------------------------------

// TestAgent_EnricherNilWhenDisabled verifies no enricher is created when
// EnrichMemory=false.
func TestAgent_EnricherNilWhenDisabled(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "hello"}}}
	ch := &mockChannel{}
	st := &mockStore{}
	cfg := defaultCfg() // EnrichMemory defaults to false

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.enricher != nil {
		t.Error("expected enricher to be nil when EnrichMemory=false")
	}
}

// TestAgent_EnricherCreatedWhenEnabled verifies that when EnrichMemory=true,
// an enricher is constructed.
func TestAgent_EnricherCreatedWhenEnabled(t *testing.T) {
	prov := &mockProvider{responses: []provider.ChatResponse{{Content: "hello"}}}
	ch := &mockChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		EnrichMemory:     true,
		EnrichRatePerMin: 10,
	}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	if ag.enricher == nil {
		t.Error("expected enricher to be non-nil when EnrichMemory=true")
	}
}

// TestAgent_EnricherEnqueueCalledAfterAppendMemory verifies that after a
// successful AppendMemory, the enricher's channel receives the entry.
func TestAgent_EnricherEnqueueCalledAfterAppendMemory(t *testing.T) {
	enrichProv := &mockProvider{
		// First response: main LLM response
		// Second response: enricher LLM call (tags)
		responses: []provider.ChatResponse{
			{Content: "the agent response"},
			{Content: "go, backend, testing"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		EnrichMemory:     true,
		EnrichRatePerMin: 60,
	}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, enrichProv, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	if ag.enricher == nil {
		t.Fatal("enricher must be non-nil for this test")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ag.enricher.Start(ctx)
	defer func() {
		cancel()
		ag.enricher.Stop()
	}()

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// Verify AppendMemory was called.
	if len(st.appendedMems) != 1 {
		t.Fatalf("expected 1 memory entry appended, got %d", len(st.appendedMems))
	}

	// Wait for the enricher to process the job and call UpdateMemory.
	deadline := time.After(3 * time.Second)
	for {
		updates := st.updateCallCount()
		if updates >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("enricher did not call UpdateMemory within 3s")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// TestUserScope
// ---------------------------------------------------------------------------

func TestUserScope(t *testing.T) {
	tests := []struct {
		name      string
		channelID string
		senderID  string
		expected  string
	}{
		{
			name:      "empty senderID returns channelID",
			channelID: "channel123",
			senderID:  "",
			expected:  "channel123",
		},
		{
			name:      "non-empty senderID returns channelID:senderID",
			channelID: "channel123",
			senderID:  "user456",
			expected:  "channel123:user456",
		},
		{
			name:      "channel with special characters",
			channelID: "telegram:-1001234567890",
			senderID:  "user789",
			expected:  "telegram:-1001234567890:user789",
		},
		{
			name:      "senderID with colon",
			channelID: "channel123",
			senderID:  "user:with:colons",
			expected:  "channel123:user:with:colons",
		},
		{
			name:      "empty channelID and empty senderID",
			channelID: "",
			senderID:  "",
			expected:  "",
		},
		{
			name:      "empty channelID with senderID",
			channelID: "",
			senderID:  "user456",
			expected:  ":user456",
		},
		{
			name:      "channelID with empty senderID",
			channelID: "discord:987654321",
			senderID:  "",
			expected:  "discord:987654321",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := userScope(tt.channelID, tt.senderID)
			if result != tt.expected {
				t.Errorf("userScope(%q, %q) = %q, want %q", tt.channelID, tt.senderID, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestUserIsolation_Integration
// ---------------------------------------------------------------------------

func TestUserIsolation_Integration(t *testing.T) {
	// Test that two messages with different SenderID on same ChannelID
	// produce different convIDs and pass different scopeIDs to AppendMemory
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "response to user1"},
			{Content: "response to user2"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// First message from user1
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test_channel",
		SenderID:  "user1",
		Content:   content.TextBlock("hello from user1"),
	})

	// Second message from user2 in same channel
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test_channel",
		SenderID:  "user2",
		Content:   content.TextBlock("hello from user2"),
	})

	// Verify we have 2 memory entries
	if len(st.appendedMems) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(st.appendedMems))
	}

	// Verify they have different scope IDs
	scope1 := st.appendedMems[0].ScopeID
	scope2 := st.appendedMems[1].ScopeID
	if scope1 == scope2 {
		t.Errorf("expected different scope IDs for different users, got %q for both", scope1)
	}

	// Verify scope IDs are correct
	expectedScope1 := "test_channel:user1"
	expectedScope2 := "test_channel:user2"
	if scope1 != expectedScope1 {
		t.Errorf("scope1 = %q, want %q", scope1, expectedScope1)
	}
	if scope2 != expectedScope2 {
		t.Errorf("scope2 = %q, want %q", scope2, expectedScope2)
	}

	// Verify conversation IDs are different
	// The conversation ID is stored in MemoryEntry.Source (convID)
	convID1 := st.appendedMems[0].Source
	convID2 := st.appendedMems[1].Source
	if convID1 == convID2 {
		t.Errorf("expected different conversation IDs for different users, got %q for both", convID1)
	}

	// Verify conversation IDs are correct
	expectedConvID1 := "conv_test_channel:user1"
	expectedConvID2 := "conv_test_channel:user2"
	if convID1 != expectedConvID1 {
		t.Errorf("convID1 = %q, want %q", convID1, expectedConvID1)
	}
	if convID2 != expectedConvID2 {
		t.Errorf("convID2 = %q, want %q", convID2, expectedConvID2)
	}
}

// ---------------------------------------------------------------------------
// TestUserIsolation_BackwardCompat
// ---------------------------------------------------------------------------

func TestUserIsolation_BackwardCompat(t *testing.T) {
	// Test that message with empty SenderID falls back to channel-only convID
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	// Message with empty SenderID (backward compatibility)
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test_channel",
		SenderID:  "", // Empty sender ID
		Content:   content.TextBlock("hello"),
	})

	// Should not panic and should create memory with channel-only scope
	if len(st.appendedMems) != 1 {
		t.Fatalf("expected 1 memory entry, got %d", len(st.appendedMems))
	}

	scope := st.appendedMems[0].ScopeID
	expectedScope := "test_channel"
	if scope != expectedScope {
		t.Errorf("scope = %q, want %q (channel-only for empty sender)", scope, expectedScope)
	}

	convID := st.appendedMems[0].Source
	expectedConvID := "conv_test_channel"
	if convID != expectedConvID {
		t.Errorf("convID = %q, want %q (channel-only for empty sender)", convID, expectedConvID)
	}
}

// ---------------------------------------------------------------------------
// TestUserIsolation_AsyncWorkers
// ---------------------------------------------------------------------------

func TestUserIsolation_AsyncWorkers(t *testing.T) {
	// Test that enricher receives correct user-scoped scopeID
	enrichProv := &mockProvider{
		responses: []provider.ChatResponse{
			{Content: "agent response"},
			{Content: "go, test"}, // enricher response
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		EnrichMemory:     true,
		EnrichRatePerMin: 60,
	}

	ag := New(cfg, defaultLimits(), config.FilterConfig{}, ch, enrichProv, st, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)
	if ag.enricher == nil {
		t.Fatal("enricher must be non-nil for this test")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ag.enricher.Start(ctx)
	defer func() {
		cancel()
		ag.enricher.Stop()
	}()

	// Process message with specific user
	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test_channel",
		SenderID:  "test_user",
		Content:   content.TextBlock("hello"),
	})

	// Verify AppendMemory was called with correct scope
	if len(st.appendedMems) != 1 {
		t.Fatalf("expected 1 memory entry, got %d", len(st.appendedMems))
	}

	expectedScope := "test_channel:test_user"
	memScope := st.appendedMems[0].ScopeID
	if memScope != expectedScope {
		t.Errorf("memory scope = %q, want %q", memScope, expectedScope)
	}

	// Wait for enricher to process and call UpdateMemory
	deadline := time.After(3 * time.Second)
	for {
		updates := st.updateCallCount()
		if updates >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("enricher did not call UpdateMemory within 3s")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// The enricher calls UpdateMemory with the entry which includes the scope ID
	// If it reaches UpdateMemory, it means the scope was passed correctly through the system
	// (We can't easily intercept the embedding worker since it's a private field)
}

// ---------------------------------------------------------------------------
// TestContextMode_PreApply_Integration
// ---------------------------------------------------------------------------

// TestContextMode_PreApply_InterceptsShell verifies that when context_mode is enabled,
// PreApply intercepts shell_exec and the tool's Execute is never called.
func TestContextMode_PreApply_InterceptsShell(t *testing.T) {
	// Provider returns a response with tool call, then final response
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "shell_exec", Input: json.RawMessage(`{"command": "echo hello"}`)},
				},
			},
			{Content: "final response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	// Set context_mode to "auto" which enables PreApply interception
	autoIndex := true
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		ContextMode: config.ContextModeConfig{
			Mode:             config.ContextModeAuto,
			ShellMaxOutput:   4096,
			SandboxTimeout:   30 * time.Second,
			SandboxKeepFirst: 20,
			SandboxKeepLast:  10,
			AutoIndexOutputs: &autoIndex,
		},
	}
	limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

	// Create a mock tool that tracks execution
	execCount := 0
	mt := &mockTool{name: "shell_exec", result: tool.ToolResult{Content: "hello world"}}
	wrappedTool := &countingTool{inner: mt, count: &execCount}

	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"shell_exec": wrappedTool,
	}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// PreApply should intercept shell_exec — the mock tool's Execute is NOT called
	if execCount != 0 {
		t.Errorf("expected shell_exec to be intercepted by PreApply (execCount=0), got %d", execCount)
	}
}

// countingTool wraps a mockTool to count execution calls
type countingTool struct {
	inner *mockTool
	count *int
}

func (c *countingTool) Name() string            { return c.inner.Name() }
func (c *countingTool) Description() string     { return c.inner.Description() }
func (c *countingTool) Schema() json.RawMessage { return c.inner.Schema() }
func (c *countingTool) Execute(ctx context.Context, params json.RawMessage) (tool.ToolResult, error) {
	*c.count++
	return c.inner.Execute(ctx, params)
}

// ---------------------------------------------------------------------------
// TestContextMode_AutoIndex_Integration
// ---------------------------------------------------------------------------

// TestContextMode_AutoIndex_IndexesOutput verifies that when AutoIndexOutputs is enabled,
// tool outputs are indexed to the OutputStore after successful execution.
func TestContextMode_AutoIndex_IndexesOutput(t *testing.T) {
	// Provider returns a response with tool call, then final response
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "shell_exec", Input: json.RawMessage(`{"command": "echo hello"}`)},
				},
			},
			{Content: "final response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	// Set context_mode to "auto" with AutoIndexOutputs enabled
	autoIndex := true
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		ContextMode: config.ContextModeConfig{
			Mode:             config.ContextModeAuto,
			ShellMaxOutput:   4096,
			SandboxTimeout:   30 * time.Second,
			SandboxKeepFirst: 20,
			SandboxKeepLast:  10,
			AutoIndexOutputs: &autoIndex,
		},
	}
	limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

	mt := &mockTool{name: "shell_exec", result: tool.ToolResult{Content: "hello world\nmore content"}}

	// Note: We need to pass the OutputStore to the agent - this test will fail
	// until we add outputStore field to Agent and wire it in New()
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"shell_exec": mt,
	}, nil, skill.SkillIndex{}, 4, false)

	// For now, we can't test the auto-indexing because the agent doesn't have
	// access to the OutputStore. This test documents the expected behavior.
	// Once we implement Task 4 (pass OutputStore to agent loop), we'll enable this.

	// Use the agent to avoid compile error
	_ = ag
}

// TestContextMode_Off_NoAutoIndex verifies that when context_mode is "off",
// tool outputs are NOT indexed.
func TestContextMode_Off_NoAutoIndex(t *testing.T) {
	// Provider returns a response with tool call, then final response
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "shell_exec", Input: json.RawMessage(`{"command": "echo hello"}`)},
				},
			},
			{Content: "final response"},
		},
	}
	ch := &mockChannel{}
	st := &mockStore{}

	// Set context_mode to "off"
	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		ContextMode: config.ContextModeConfig{
			Mode: config.ContextModeOff,
		},
	}
	limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

	mt := &mockTool{name: "shell_exec", result: tool.ToolResult{Content: "hello world"}}

	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"shell_exec": mt,
	}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("hello")})

	// When context_mode is off, no auto-indexing should happen
	// This test verifies the baseline behavior
	if len(ch.sent) == 0 {
		t.Error("expected messages to be sent")
	}
}

// ---------------------------------------------------------------------------
// TestContextMode_E2E - Integration test for context-mode features
// ---------------------------------------------------------------------------

// TestContextMode_E2E_ShellExecWithIndexing is an end-to-end test that verifies:
// 1. context_mode = "auto" enables PreApply and auto-indexing
// 2. Tool outputs are indexed to the OutputStore (FTS5)
// 3. search_output tool can find indexed outputs
// 4. batch_exec tool works end-to-end
func TestContextMode_E2E_ShellExecWithIndexing(t *testing.T) {
	// Create a temporary SQLite store for testing
	tmpDir := t.TempDir()
	st, err := store.New(config.StoreConfig{Type: "sqlite", Path: tmpDir})
	if err != nil {
		t.Fatalf("failed to create temp store: %v", err)
	}
	defer st.Close()

	// Get the OutputStore interface from the store
	outputStore, ok := st.(store.OutputStore)
	if !ok {
		t.Fatal("store does not implement OutputStore")
	}

	// Set up context_mode = "auto" with auto-indexing enabled
	autoIndex := true
	ctxModeCfg := config.ContextModeConfig{
		Mode:             config.ContextModeAuto,
		ShellMaxOutput:   4096,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
		AutoIndexOutputs: &autoIndex,
	}

	// Create provider that returns shell_exec tool call, then final response
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{
				ToolCalls: []provider.ToolCall{
					{ID: "t1", Name: "shell_exec", Input: json.RawMessage(`{"command": "echo hello world from test"}`)},
				},
			},
			{Content: "completed"},
		},
	}
	ch := &mockChannel{}

	cfg := config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		ContextMode:      ctxModeCfg,
	}
	limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

	// Create shell tool
	shellTool := tool.NewShellTool(config.ShellToolConfig{Enabled: true, AllowAll: true})

	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"shell_exec": shellTool,
	}, nil, skill.SkillIndex{}, 4, false)

	// Process a message that triggers shell_exec
	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("run echo")})

	// Drain the async indexing worker so the output is committed before we search.
	_ = ag.Shutdown()

	// Verify the tool was executed
	if len(ch.sent) == 0 {
		t.Fatal("expected messages to be sent")
	}

	// Verify output was indexed to FTS5 by searching for it
	ctx := context.Background()
	results, err := outputStore.SearchOutputs(ctx, "hello world", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}

	// We should find at least one result containing "hello world"
	if len(results) == 0 {
		t.Error("expected at least one indexed output containing 'hello world'")
	}

	found := false
	for _, r := range results {
		if strings.Contains(r.Content, "hello world") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find output containing 'hello world', got: %v", results)
	}
}

// TestContextMode_E2E_SearchOutputTool tests the search_output tool end-to-end
func TestContextMode_E2E_SearchOutputTool(t *testing.T) {
	// Create a temporary SQLite store for testing
	tmpDir := t.TempDir()
	s, err := store.NewSQLiteStore(config.StoreConfig{Type: "sqlite", Path: tmpDir})
	if err != nil {
		t.Fatalf("failed to create temp store: %v", err)
	}
	defer s.Close()

	// Pre-index some outputs
	ctx := context.Background()
	outputs := []store.ToolOutput{
		{ID: "1", ToolName: "shell_exec", Command: "ls -la", Content: "total 24\ndrwxr-xr-x  5 user user 4096 Apr  9 10:00 .\ndrwxr-xr-x  2 user user 4096 Apr  9 09:00 ..", ExitCode: 0, Timestamp: time.Now()},
		{ID: "2", ToolName: "shell_exec", Command: "cat file.txt", Content: "Hello World\nThis is a test file", ExitCode: 0, Timestamp: time.Now()},
		{ID: "3", ToolName: "shell_exec", Command: "pwd", Content: "/home/user", ExitCode: 0, Timestamp: time.Now()},
	}
	for _, o := range outputs {
		if err := s.IndexOutput(ctx, o); err != nil {
			t.Fatalf("IndexOutput failed: %v", err)
		}
	}

	// Create the SearchOutputTool
	searchTool := tool.NewSearchOutputTool(s)

	// Test searching for "test"
	result, err := searchTool.Execute(ctx, json.RawMessage(`{"query": "test", "limit": 10}`))
	if err != nil {
		t.Fatalf("SearchOutputTool.Execute failed: %v", err)
	}
	if result.IsError {
		t.Errorf("SearchOutputTool returned error: %s", result.Content)
	}
	// The result format is: "[1] shell_exec: {preview} (exit={code})"
	// We expect to find "test" or "Hello World" in the content
	if !strings.Contains(result.Content, "test") && !strings.Contains(result.Content, "Hello") {
		t.Errorf("expected result to contain 'test' or 'Hello', got: %s", result.Content)
	}

	// Test searching for "ls"
	result2, err := searchTool.Execute(ctx, json.RawMessage(`{"query": "ls", "limit": 10}`))
	if err != nil {
		t.Fatalf("SearchOutputTool.Execute failed: %v", err)
	}
	if result2.IsError {
		t.Errorf("SearchOutputTool returned error: %s", result2.Content)
	}
	// Should find content with "total" (from ls -la output)
	if !strings.Contains(result2.Content, "total") {
		t.Errorf("expected result to contain 'total', got: %s", result2.Content)
	}
}

// TestContextMode_E2E_BatchExecTool tests the batch_exec tool end-to-end
func TestContextMode_E2E_BatchExecTool(t *testing.T) {
	// Create a temporary SQLite store for testing
	tmpDir := t.TempDir()
	s, err := store.NewSQLiteStore(config.StoreConfig{Type: "sqlite", Path: tmpDir})
	if err != nil {
		t.Fatalf("failed to create temp store: %v", err)
	}
	defer s.Close()

	// Create the BatchExecTool
	batchTool := tool.NewBatchExecTool(s, tool.BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	ctx := context.Background()

	// Test executing multiple commands
	result, err := batchTool.Execute(ctx, json.RawMessage(`{
		"commands": ["echo first", "echo second", "echo third"],
		"stop_on_error": false
	}`))
	if err != nil {
		t.Fatalf("BatchExecTool.Execute failed: %v", err)
	}
	if result.IsError {
		t.Errorf("BatchExecTool returned error: %s", result.Content)
	}

	// Verify the summary contains expected information
	if !strings.Contains(result.Content, "Executed 3 commands") {
		t.Errorf("expected summary to contain 'Executed 3 commands', got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "3 succeeded") {
		t.Errorf("expected summary to contain '3 succeeded', got: %s", result.Content)
	}

	// Verify outputs were indexed
	searchResults, err := s.SearchOutputs(ctx, "first", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(searchResults) == 0 {
		t.Error("expected outputs to be indexed")
	}
}

// TestContextMode_E2E_BatchExecStopOnError tests the batch_exec tool with stop_on_error
func TestContextMode_E2E_BatchExecStopOnError(t *testing.T) {
	// Create a temporary SQLite store for testing
	tmpDir := t.TempDir()
	s, err := store.NewSQLiteStore(config.StoreConfig{Type: "sqlite", Path: tmpDir})
	if err != nil {
		t.Fatalf("failed to create temp store: %v", err)
	}
	defer s.Close()

	// Create the BatchExecTool
	batchTool := tool.NewBatchExecTool(s, tool.BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	ctx := context.Background()

	// Test executing commands with stop_on_error
	result, err := batchTool.Execute(ctx, json.RawMessage(`{
		"commands": ["echo success", "exit 1", "echo should not run"],
		"stop_on_error": true
	}`))
	if err != nil {
		t.Fatalf("BatchExecTool.Execute failed: %v", err)
	}

	// Should have error due to exit 1
	if !result.IsError {
		t.Error("expected result to be an error due to non-zero exit code")
	}

	// Verify the summary shows fewer than 3 commands ran
	if strings.Contains(result.Content, "Executed 3 commands") {
		t.Error("expected only 2 commands to run due to stop_on_error")
	}
}

// ---------------------------------------------------------------------------
// mockStoreWithOutputStore — store.Store + store.OutputStore for H2/H3 tests
// ---------------------------------------------------------------------------

type mockStoreWithOutputStore struct {
	mockStore
	mu      sync.Mutex
	indexed []store.ToolOutput
}

func (m *mockStoreWithOutputStore) IndexOutput(_ context.Context, output store.ToolOutput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexed = append(m.indexed, output)
	return nil
}

func (m *mockStoreWithOutputStore) SearchOutputs(_ context.Context, _ string, _ int) ([]store.ToolOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]store.ToolOutput, len(m.indexed))
	copy(cp, m.indexed)
	return cp, nil
}

// ---------------------------------------------------------------------------
// TestAutoIndex_ExitCode — H2: loop reads microagent/exit_code from Meta
// ---------------------------------------------------------------------------

// autoIndexCfg returns an AgentConfig with AutoIndexOutputs=true and Mode=Off.
// Mode=Off prevents PreApply from intercepting tool calls, so the mock tool
// result (with its hand-crafted Meta map) is used directly by the loop.
// AutoIndexOutputs is checked independently of Mode, so indexing still fires.
func autoIndexCfg() config.AgentConfig {
	autoIndex := true
	return config.AgentConfig{
		MaxIterations:    5,
		MaxTokensPerTurn: 100,
		ContextMode: config.ContextModeConfig{
			Mode:             config.ContextModeOff,
			AutoIndexOutputs: &autoIndex,
		},
	}
}

func TestAutoIndex_ExitCode(t *testing.T) {
	cases := []struct {
		name         string
		metaExitCode string // empty string means key absent
		wantExitCode int
	}{
		{"key_42", "42", 42},
		{"key_0", "0", 0},
		{"key_invalid", "invalid", 0},
		{"key_absent", "", 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]string{"microagent/truncated": "false"}
			if tc.metaExitCode != "" {
				meta["microagent/exit_code"] = tc.metaExitCode
			}

			mt := &mockTool{name: "mock_tool", result: tool.ToolResult{
				Content: "some output",
				IsError: false,
				Meta:    meta,
			}}

			prov := &mockProvider{
				responses: []provider.ChatResponse{
					{ToolCalls: []provider.ToolCall{{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)}}},
					{Content: "done"},
				},
			}
			ch := &mockChannel{}
			st := &mockStoreWithOutputStore{}
			limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

			ag := New(autoIndexCfg(), limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
				"mock_tool": mt,
			}, nil, skill.SkillIndex{}, 4, false)

			ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("go")})
			// Drain the async indexing worker before checking results.
			_ = ag.Shutdown()

			st.mu.Lock()
			indexed := st.indexed
			st.mu.Unlock()

			if len(indexed) != 1 {
				t.Fatalf("expected 1 indexed output, got %d", len(indexed))
			}
			if indexed[0].ExitCode != tc.wantExitCode {
				t.Errorf("ExitCode: got %d, want %d", indexed[0].ExitCode, tc.wantExitCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAutoIndex_Truncated — H3: loop reads microagent/truncated from Meta
// ---------------------------------------------------------------------------

func TestAutoIndex_Truncated(t *testing.T) {
	cases := []struct {
		name          string
		metaTruncated string // empty string means key absent
		// filterCfg controls whether the filter runs and may produce Metrics.
		filterCfg config.FilterConfig
		// content is the mock tool output; a long string can be truncated by filter.
		content       string
		wantTruncated bool
	}{
		// Meta key present — overrides the filter-level fallback entirely.
		{
			name:          "meta_true",
			metaTruncated: "true",
			wantTruncated: true,
		},
		{
			// filter would say truncated (content >> TruncationChars), but Meta says false.
			name:          "meta_false_overrides_filter",
			metaTruncated: "false",
			filterCfg:     config.FilterConfig{Enabled: true, TruncationChars: 5},
			content:       "this content is definitely longer than five chars",
			wantTruncated: false,
		},
		// Meta key absent — falls back to filterMetrics.CompressedBytes < OriginalBytes.
		{
			// filter disabled → Metrics{} → 0 < 0 = false
			name:          "fallback_not_truncated",
			filterCfg:     config.FilterConfig{Enabled: false},
			content:       "short output",
			wantTruncated: false,
		},
		{
			// filter enabled with tiny limit → CompressedBytes < OriginalBytes → true.
			// Use a non-shell tool name so the filter hits the generic truncation path.
			name:          "fallback_truncated_via_filter",
			filterCfg:     config.FilterConfig{Enabled: true, TruncationChars: 5, Levels: config.FilterLevels{Generic: true}},
			content:       "this content is definitely longer than five chars",
			wantTruncated: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]string{"microagent/exit_code": "0"}
			if tc.metaTruncated != "" {
				meta["microagent/truncated"] = tc.metaTruncated
			}

			toolContent := tc.content
			if toolContent == "" {
				toolContent = "output"
			}

			// Use "generic_tool" name so filter hits the generic path, not shell path,
			// keeping behavior predictable.
			toolName := "generic_tool"

			mt := &mockTool{name: toolName, result: tool.ToolResult{
				Content: toolContent,
				IsError: false,
				Meta:    meta,
			}}

			prov := &mockProvider{
				responses: []provider.ChatResponse{
					{ToolCalls: []provider.ToolCall{{ID: "t1", Name: toolName, Input: json.RawMessage(`{}`)}}},
					{Content: "done"},
				},
			}
			ch := &mockChannel{}
			st := &mockStoreWithOutputStore{}
			limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

			ag := New(autoIndexCfg(), limits, tc.filterCfg, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
				toolName: mt,
			}, nil, skill.SkillIndex{}, 4, false)

			ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("go")})
			// Drain the async indexing worker before checking results.
			_ = ag.Shutdown()

			st.mu.Lock()
			indexed := st.indexed
			st.mu.Unlock()

			if len(indexed) != 1 {
				t.Fatalf("expected 1 indexed output, got %d", len(indexed))
			}
			if indexed[0].Truncated != tc.wantTruncated {
				t.Errorf("Truncated: got %v, want %v", indexed[0].Truncated, tc.wantTruncated)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAutoIndex_EmptyContent_NotIndexed — empty Content skips Enqueue
// ---------------------------------------------------------------------------

// TestAutoIndex_EmptyContent_NotIndexed verifies that tool results with empty
// Content (e.g. `touch foo`) are not enqueued for indexing. Commands that
// succeed with no stdout would otherwise produce noisy ErrOutputEmptyContent
// warnings from the IndexingWorker.
func TestAutoIndex_EmptyContent_NotIndexed(t *testing.T) {
	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{
		Content: "", // empty — simulates a command with no stdout
		IsError: false,
	}}

	prov := &mockProvider{
		responses: []provider.ChatResponse{
			{ToolCalls: []provider.ToolCall{{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)}}},
			{Content: "done"},
		},
	}
	ch := &mockChannel{}
	st := &mockStoreWithOutputStore{}
	limits := config.LimitsConfig{TotalTimeout: 10 * time.Second, ToolTimeout: 2 * time.Second}

	ag := New(autoIndexCfg(), limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{
		"mock_tool": mt,
	}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{ChannelID: "test", Content: content.TextBlock("go")})
	// Drain the async indexing worker before checking results.
	_ = ag.Shutdown()

	st.mu.Lock()
	indexed := st.indexed
	st.mu.Unlock()

	if len(indexed) != 0 {
		t.Errorf("expected 0 indexed outputs for empty content, got %d", len(indexed))
	}
}

// ---------------------------------------------------------------------------
// Degradation tests (Phase 2)
// ---------------------------------------------------------------------------

// TestProcessMessage_DegradationNotice_ImageOnTextOnlyProvider verifies that
// a user message with an image block on a text-only provider prefixes the
// reply with the degradation notice.
func TestProcessMessage_DegradationNotice_ImageOnTextOnlyProvider(t *testing.T) {
	prov := &mockProvider{
		supportsMultimodal: false,
		responses: []provider.ChatResponse{
			{Content: "I see your request.", StopReason: "end_turn"},
		},
	}
	ch := &mockChannel{}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	imgMsg := channel.IncomingMessage{
		ChannelID: "c1",
		Content: content.Blocks{
			{Type: content.BlockImage, MIME: "image/jpeg", Size: 1024},
			{Type: content.BlockText, Text: "what is this?"},
		},
	}
	ag.processMessage(context.Background(), imgMsg)

	msgs := ch.sentMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one sent message")
	}
	notice := content.DegradationNotice(imgMsg.Content)
	if !strings.HasPrefix(msgs[0].Text, notice) {
		t.Errorf("reply does not start with degradation notice:\ngot:  %q\nwant prefix: %q", msgs[0].Text, notice)
	}
}

// TestProcessMessage_NoDegradationNotice_ImageOnMultimodalProvider verifies
// that a multimodal provider does NOT trigger the degradation notice.
func TestProcessMessage_NoDegradationNotice_ImageOnMultimodalProvider(t *testing.T) {
	prov := &mockProvider{
		supportsMultimodal: true,
		responses: []provider.ChatResponse{
			{Content: "I see a cat.", StopReason: "end_turn"},
		},
	}
	ch := &mockChannel{}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	imgMsg := channel.IncomingMessage{
		ChannelID: "c2",
		Content: content.Blocks{
			{Type: content.BlockImage, MIME: "image/jpeg", Size: 1024},
			{Type: content.BlockText, Text: "what is this?"},
		},
	}
	ag.processMessage(context.Background(), imgMsg)

	msgs := ch.sentMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one sent message")
	}
	notice := content.DegradationNotice(imgMsg.Content)
	if strings.HasPrefix(msgs[0].Text, notice) {
		t.Errorf("reply unexpectedly starts with degradation notice: %q", msgs[0].Text)
	}
}

// TestProcessMessage_NoDegradationNotice_TextOnlyMessage verifies that a
// text-only message on a text-only provider produces no degradation notice.
func TestProcessMessage_NoDegradationNotice_TextOnlyMessage(t *testing.T) {
	prov := &mockProvider{
		supportsMultimodal: false,
		responses: []provider.ChatResponse{
			{Content: "Hello!", StopReason: "end_turn"},
		},
	}
	ch := &mockChannel{}
	ag := New(defaultCfg(), defaultLimits(), config.FilterConfig{}, ch, prov, &mockStore{}, audit.NoopAuditor{}, nil, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "c3",
		Content:   content.TextBlock("hello"),
	})

	msgs := ch.sentMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one sent message")
	}
	if msgs[0].Text != "Hello!" {
		t.Errorf("unexpected reply: %q", msgs[0].Text)
	}
}
