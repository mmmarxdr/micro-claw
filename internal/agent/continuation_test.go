package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/skill"
	"daimon/internal/tool"
)

// telemetryChannel is a mockChannel that also implements TelemetryEmitter so
// the agent's iteration-limit path exercises the UI branch (pill event) rather
// than the legacy text-message fallback.
type telemetryChannel struct {
	mockChannel
	mu     sync.Mutex
	frames []map[string]any
}

func (t *telemetryChannel) EmitTelemetry(_ context.Context, channelID string, frame map[string]any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// store a shallow copy so later mutations by the caller don't race with assertions
	cp := make(map[string]any, len(frame))
	for k, v := range frame {
		cp[k] = v
	}
	cp["channel_id"] = channelID
	t.frames = append(t.frames, cp)
	return nil
}

func (t *telemetryChannel) framesByType(typ string) []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []map[string]any
	for _, f := range t.frames {
		if f["type"] == typ {
			out = append(out, f)
		}
	}
	return out
}

// TestProcessMessage_IterationLimitEmitsTelemetry — when the agent hits the
// iteration cap AND the channel supports telemetry, the UI-facing
// `iteration_limit_reached` event fires instead of the legacy text message.
func TestProcessMessage_IterationLimitEmitsTelemetry(t *testing.T) {
	toolCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{toolCall, toolCall, toolCall},
	}
	ch := &telemetryChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{MaxIterations: 2, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	events := ch.framesByType("iteration_limit_reached")
	if len(events) != 1 {
		t.Fatalf("expected 1 iteration_limit_reached event, got %d (frames: %v)", len(events), ch.frames)
	}
	if ev := events[0]; ev["iterations"] != 2 {
		t.Errorf("iterations=%v, want 2", ev["iterations"])
	}
	// Legacy text fallback MUST NOT fire when telemetry is available — the UI
	// would end up with both a pill and a dead-end text bubble.
	for _, m := range ch.sentMessages() {
		if m.Text == "(iteration limit reached)" {
			t.Errorf("legacy text fallback fired even though telemetry channel is wired")
		}
	}
}

// TestProcessMessage_TokenBudgetEmitsTelemetry — when MaxTotalTokens is set
// and the cumulative tokens cross the budget, the UI-facing
// `token_budget_reached` event fires and the loop stops.
func TestProcessMessage_TokenBudgetEmitsTelemetry(t *testing.T) {
	// Each LLM call reports 60 tokens (50 in + 10 out). With a 100-token
	// budget the loop must stop after the second iteration at the latest.
	toolCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
		Usage: provider.UsageStats{InputTokens: 50, OutputTokens: 10},
	}
	prov := &mockProvider{
		responses: []provider.ChatResponse{toolCall, toolCall, toolCall, toolCall, toolCall},
	}
	ch := &telemetryChannel{}
	st := &mockStore{}
	// No iteration cap — only the token budget should terminate.
	cfg := config.AgentConfig{MaxIterations: 0, MaxTotalTokens: 100, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	budgetEvents := ch.framesByType("token_budget_reached")
	if len(budgetEvents) != 1 {
		t.Fatalf("expected 1 token_budget_reached event, got %d (frames: %v)", len(budgetEvents), ch.frames)
	}
	ev := budgetEvents[0]
	if ev["budget"] != 100 {
		t.Errorf("budget=%v, want 100", ev["budget"])
	}
	if consumed, ok := ev["consumed_tokens"].(int); !ok || consumed < 100 {
		t.Errorf("consumed_tokens=%v, want >=100", ev["consumed_tokens"])
	}
	// The iteration-limit event MUST NOT fire — this turn was bounded by
	// the token budget, not by the iteration cap.
	if iter := ch.framesByType("iteration_limit_reached"); len(iter) != 0 {
		t.Errorf("iteration_limit_reached fired alongside token_budget_reached: %v", iter)
	}
}

// TestProcessMessage_NoDefaultIterationCap — with MaxIterations=0 and no token
// budget, the loop is only bounded by the total-timeout context and by the
// provider returning a terminal (text-only) response.
func TestProcessMessage_NoDefaultIterationCap(t *testing.T) {
	// Provider returns 15 tool calls then a final text response — previously
	// this would have hit the legacy default cap of 10; now it runs to completion.
	toolCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
	}
	responses := make([]provider.ChatResponse, 0, 16)
	for i := 0; i < 15; i++ {
		responses = append(responses, toolCall)
	}
	responses = append(responses, provider.ChatResponse{Content: "done"})

	prov := &mockProvider{responses: responses}
	ch := &telemetryChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{MaxIterations: 0, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	// No pause event of any kind — the turn ran to natural completion.
	if events := ch.framesByType("iteration_limit_reached"); len(events) != 0 {
		t.Errorf("iteration_limit_reached fired with no cap configured: %v", events)
	}
	if events := ch.framesByType("token_budget_reached"); len(events) != 0 {
		t.Errorf("token_budget_reached fired with no budget configured: %v", events)
	}
	// And the provider was called for all 16 planned responses (15 tool +
	// 1 final). If the default cap still fired at 10, we'd see fewer calls.
	if got := prov.callCount(); got != 16 {
		t.Errorf("provider call count = %d, want 16 (cap should not have fired)", got)
	}
}

// TestProcessMessage_LoopDetectionEmitsOnceAfterThreshold — three identical
// tool calls (same name, same input) within the window should fire exactly
// ONE `loop_detected` event. Subsequent repeats of the same pattern must NOT
// re-fire (the alert is a heads-up, not a stream of noise). Distinct inputs
// should NOT trigger it.
func TestProcessMessage_LoopDetectionEmitsOnceAfterThreshold(t *testing.T) {
	// Same tool + same input three times, then once with a different input,
	// then a final text reply so the loop terminates naturally.
	sameCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{"arg":"a"}`)},
		},
	}
	differentCall := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t2", Name: "mock_tool", Input: json.RawMessage(`{"arg":"b"}`)},
		},
	}
	textReply := provider.ChatResponse{Content: "done"}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			sameCall, sameCall, sameCall,   // 3rd repeat → fires loop_detected
			sameCall,                        // 4th repeat → must NOT re-fire
			differentCall,                   // different input — unaffected
			textReply,
		},
	}
	ch := &telemetryChannel{}
	st := &mockStore{}
	// No caps — want the loop to run its course and observe the alert.
	cfg := config.AgentConfig{MaxIterations: 0, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	events := ch.framesByType("loop_detected")
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 loop_detected event, got %d (frames: %v)", len(events), ch.frames)
	}
	ev := events[0]
	if ev["tool_name"] != "mock_tool" {
		t.Errorf("tool_name=%v, want mock_tool", ev["tool_name"])
	}
	if reps, ok := ev["repetitions"].(int); !ok || reps < 3 {
		t.Errorf("repetitions=%v, want >=3", ev["repetitions"])
	}
	// sample_input present + comes from the repeated call.
	if sample, _ := ev["sample_input"].(string); sample == "" {
		t.Errorf("sample_input missing or empty")
	}
}

// TestProcessMessage_LoopDetectionIgnoresVariedInputs — calls to the same
// tool with DIFFERENT inputs must never trigger a loop warning, even if
// there are many of them. That's normal multi-file exploration, not a loop.
func TestProcessMessage_LoopDetectionIgnoresVariedInputs(t *testing.T) {
	mk := func(arg string) provider.ChatResponse {
		return provider.ChatResponse{
			ToolCalls: []provider.ToolCall{
				{ID: "t", Name: "mock_tool", Input: json.RawMessage(fmt.Sprintf(`{"arg":%q}`, arg))},
			},
		}
	}
	textReply := provider.ChatResponse{Content: "done"}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			mk("a"), mk("b"), mk("c"), mk("d"), mk("e"), mk("f"),
			textReply,
		},
	}
	ch := &telemetryChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{MaxIterations: 0, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ag.processMessage(context.Background(), channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	if events := ch.framesByType("loop_detected"); len(events) != 0 {
		t.Errorf("loop_detected fired with all-distinct inputs: %v", events)
	}
}

// TestProcessMessage_ContinuationSkipsUserAppend — a continuation IncomingMessage
// must NOT add a new user turn to the conversation; it resumes the existing one.
func TestProcessMessage_ContinuationSkipsUserAppend(t *testing.T) {
	// First turn: provider returns a tool call once, then a final text reply on
	// the second iteration. That second iteration is what the continuation will
	// drive, so we need enough responses queued for both turns.
	toolResp := provider.ChatResponse{
		ToolCalls: []provider.ToolCall{
			{ID: "t1", Name: "mock_tool", Input: json.RawMessage(`{}`)},
		},
	}
	textResp := provider.ChatResponse{Content: "done"}
	prov := &mockProvider{
		responses: []provider.ChatResponse{
			// First turn (user=go, cap=1 → hits cap after the single tool call).
			toolResp,
			// Continuation turn — provider sees the tool result and replies.
			textResp,
		},
	}
	ch := &telemetryChannel{}
	st := &mockStore{}
	cfg := config.AgentConfig{MaxIterations: 1, MaxTokensPerTurn: 100}
	limits := config.LimitsConfig{TotalTimeout: 5 * time.Second, ToolTimeout: 1 * time.Second}

	mt := &mockTool{name: "mock_tool", result: tool.ToolResult{Content: "ok"}}
	ag := New(cfg, limits, config.FilterConfig{}, ch, prov, st, audit.NoopAuditor{}, map[string]tool.Tool{"mock_tool": mt}, nil, skill.SkillIndex{}, 4, false)

	ctx := context.Background()
	// First turn — hits the cap.
	ag.processMessage(ctx, channel.IncomingMessage{
		ChannelID: "test",
		Content:   content.TextBlock("go"),
	})

	convID := "conv_test:"
	conv, err := st.LoadConversation(ctx, convID)
	if err != nil {
		t.Fatalf("load conversation after first turn: %v", err)
	}
	userCountBefore := 0
	for _, m := range conv.Messages {
		if m.Role == "user" {
			userCountBefore++
		}
	}
	if userCountBefore != 1 {
		t.Fatalf("expected 1 user message after first turn, got %d", userCountBefore)
	}

	// Continuation — must NOT add a second user message.
	ag.processMessage(ctx, channel.IncomingMessage{
		ChannelID:      "test",
		IsContinuation: true,
	})

	conv, err = st.LoadConversation(ctx, convID)
	if err != nil {
		t.Fatalf("load conversation after continuation: %v", err)
	}
	userCountAfter := 0
	for _, m := range conv.Messages {
		if m.Role == "user" {
			userCountAfter++
		}
	}
	if userCountAfter != 1 {
		t.Errorf("continuation must not append a user message; user messages before=%d after=%d", userCountBefore, userCountAfter)
	}
}
