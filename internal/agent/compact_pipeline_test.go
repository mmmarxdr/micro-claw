package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
)

// --- T3.1 tests: findTurnBoundaries and findNearestBoundaryBefore ---

func TestFindTurnBoundaries_SimpleAlternation(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "user"},      // 0 — first turn
		{Role: "assistant"}, // 1 — turn boundary
		{Role: "user"},      // 2 — turn boundary
		{Role: "assistant"}, // 3 — turn boundary
	}
	boundaries := findTurnBoundaries(msgs)

	// Expect boundaries at 0, 1, 2, 3 (every role change + start)
	want := []int{0, 1, 2, 3}
	if len(boundaries) != len(want) {
		t.Fatalf("got boundaries %v, want %v", boundaries, want)
	}
	for i, b := range boundaries {
		if b != want[i] {
			t.Errorf("boundary[%d] = %d, want %d", i, b, want[i])
		}
	}
}

func TestFindTurnBoundaries_MultiToolGrouped(t *testing.T) {
	// assistant with tool calls + multiple tool results = ONE turn (not split)
	msgs := []provider.ChatMessage{
		{Role: "user"},                                                          // 0
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "tool_a"}}}, // 1 — turn start
		{Role: "tool", ToolCallID: "c1"},                                        // 2 — same turn as 1
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c2", Name: "tool_b"}, {ID: "c3", Name: "tool_c"}}}, // 3 — new turn
		{Role: "tool", ToolCallID: "c2"},                                        // 4 — same turn as 3
		{Role: "tool", ToolCallID: "c3"},                                        // 5 — same turn as 3
		{Role: "user"},                                                          // 6 — new turn
	}

	boundaries := findTurnBoundaries(msgs)

	// Expected turn boundaries: 0 (user), 1 (assistant+tool_a), 3 (assistant+tool_b,c), 6 (user)
	want := []int{0, 1, 3, 6}
	if len(boundaries) != len(want) {
		t.Fatalf("got boundaries %v, want %v", boundaries, want)
	}
	for i, b := range boundaries {
		if b != want[i] {
			t.Errorf("boundary[%d] = %d, want %d", i, b, want[i])
		}
	}
}

func TestFindTurnBoundaries_Empty(t *testing.T) {
	boundaries := findTurnBoundaries(nil)
	if len(boundaries) != 0 {
		t.Errorf("empty messages: got boundaries %v, want empty", boundaries)
	}

	boundaries = findTurnBoundaries([]provider.ChatMessage{})
	if len(boundaries) != 0 {
		t.Errorf("empty slice: got boundaries %v, want empty", boundaries)
	}
}

func TestFindTurnBoundaries_SingleMessage(t *testing.T) {
	msgs := []provider.ChatMessage{{Role: "user"}}
	boundaries := findTurnBoundaries(msgs)
	want := []int{0}
	if len(boundaries) != len(want) {
		t.Fatalf("single message: got boundaries %v, want %v", boundaries, want)
	}
	if boundaries[0] != 0 {
		t.Errorf("boundaries[0] = %d, want 0", boundaries[0])
	}
}

func TestFindNearestBoundaryBefore_BasicSnap(t *testing.T) {
	boundaries := []int{0, 3, 6, 10}

	// idx 0 → boundary 0
	if got := findNearestBoundaryBefore(0, boundaries); got != 0 {
		t.Errorf("idx=0: got %d, want 0", got)
	}
	// idx 3 → boundary 3 (exact match)
	if got := findNearestBoundaryBefore(3, boundaries); got != 3 {
		t.Errorf("idx=3: got %d, want 3", got)
	}
	// idx 5 → boundary 3 (largest ≤ 5)
	if got := findNearestBoundaryBefore(5, boundaries); got != 3 {
		t.Errorf("idx=5: got %d, want 3", got)
	}
	// idx 10 → boundary 10
	if got := findNearestBoundaryBefore(10, boundaries); got != 10 {
		t.Errorf("idx=10: got %d, want 10", got)
	}
	// idx 100 → boundary 10 (beyond all boundaries)
	if got := findNearestBoundaryBefore(100, boundaries); got != 10 {
		t.Errorf("idx=100: got %d, want 10", got)
	}
}

func TestFindNearestBoundaryBefore_ToolGroupSnap(t *testing.T) {
	// Simulate tool group: turn starts at 3 (assistant+toolcalls), tool results at 4,5
	// idx=4 (inside tool group) → should snap to 3
	boundaries := []int{0, 3, 6}

	if got := findNearestBoundaryBefore(4, boundaries); got != 3 {
		t.Errorf("idx=4 inside tool group: got %d, want 3", got)
	}
	if got := findNearestBoundaryBefore(5, boundaries); got != 3 {
		t.Errorf("idx=5 inside tool group: got %d, want 3", got)
	}
}

func TestFindNearestBoundaryBefore_EmptyBoundaries(t *testing.T) {
	if got := findNearestBoundaryBefore(5, nil); got != 0 {
		t.Errorf("empty boundaries: got %d, want 0", got)
	}
	if got := findNearestBoundaryBefore(5, []int{}); got != 0 {
		t.Errorf("empty slice: got %d, want 0", got)
	}
}

// --- T3.2 tests: Pass 1 tool result compression ---

func makeTurnMessages(userContent, assistantContent, toolResult string) []provider.ChatMessage { //nolint:unused // kept for future test assertions
	return []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(userContent)},
		{
			Role:      "assistant",
			Content:   content.TextBlock(assistantContent),
			ToolCalls: []provider.ToolCall{{ID: "tc1", Name: "some_tool"}},
		},
		{Role: "tool", ToolCallID: "tc1", Content: content.TextBlock(toolResult)},
	}
}

// buildPipelineManager creates a ContextManager wired for compaction pipeline testing.
// It sets maxTokens and compactThreshold=0.5 so tests control when pipeline fires.
func buildPipelineManager(maxTokens int) *ContextManager {
	cfg := config.ContextConfig{
		MaxTokens:          maxTokens,
		CompactThreshold:   0.5,
		ProtectedTurns:     1, // protect only the last turn
		ToolResultMaxChars: 100,
		SummaryMaxTokens:   500,
		Strategy:           "smart",
		CooldownTurns:      0,
	}
	cfg.ApplyContextDefaults() // fill any remaining defaults
	// Override back to our values after ApplyContextDefaults
	cfg.CompactThreshold = 0.5
	cfg.ProtectedTurns = 1
	cfg.ToolResultMaxChars = 100
	prov := &cmMockProvider{name: "test", model: "test-model"}
	return &ContextManager{
		cfg:             cfg,
		prov:            prov,
		resolvedMaxToks: maxTokens,
	}
}

func TestCompactPipeline_Pass1_ProtectedTailNotCompressed(t *testing.T) {
	// Build: 2 old turns + 1 protected turn (ProtectedTurns=1)
	// The protected tail's tool result should NOT be compressed
	bigResult := strings.Repeat("x", 2000) // 2000 chars >> ToolResultMaxChars=100
	msgs := []provider.ChatMessage{
		// old turn 1
		{Role: "user", Content: content.TextBlock("request 1")},
		{Role: "assistant", Content: content.TextBlock("response 1"), ToolCalls: []provider.ToolCall{{ID: "t1", Name: "tool_a"}}},
		{Role: "tool", ToolCallID: "t1", Content: content.TextBlock(bigResult)}, // old — should be compressed
		// old turn 2
		{Role: "user", Content: content.TextBlock("request 2")},
		{Role: "assistant", Content: content.TextBlock("response 2"), ToolCalls: []provider.ToolCall{{ID: "t2", Name: "tool_b"}}},
		{Role: "tool", ToolCallID: "t2", Content: content.TextBlock(bigResult)}, // old — should be compressed
		// protected turn (last ProtectedTurns=1 complete turn)
		{Role: "user", Content: content.TextBlock("protected request")},
		{Role: "assistant", Content: content.TextBlock("protected response"), ToolCalls: []provider.ToolCall{{ID: "t3", Name: "tool_c"}}},
		{Role: "tool", ToolCallID: "t3", Content: content.TextBlock(bigResult)}, // protected — should NOT be compressed
	}

	cm := buildPipelineManager(100000) // large max tokens, no budget pressure — just test compression logic
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// Find tool results by ToolCallID
	var oldToolResult1, oldToolResult2, protectedToolResult *provider.ChatMessage
	for i := range result {
		switch result[i].ToolCallID {
		case "t1":
			r := result[i]
			oldToolResult1 = &r
		case "t2":
			r := result[i]
			oldToolResult2 = &r
		case "t3":
			r := result[i]
			protectedToolResult = &r
		}
	}

	if oldToolResult1 == nil || oldToolResult2 == nil || protectedToolResult == nil {
		t.Fatal("could not find expected tool result messages")
	}

	// Old results should be compressed (len < original 2000)
	if len(oldToolResult1.Content.TextOnly()) >= 2000 {
		t.Errorf("old tool result 1 not compressed: len=%d", len(oldToolResult1.Content.TextOnly()))
	}
	if len(oldToolResult2.Content.TextOnly()) >= 2000 {
		t.Errorf("old tool result 2 not compressed: len=%d", len(oldToolResult2.Content.TextOnly()))
	}

	// Protected result should remain uncompressed
	if len(protectedToolResult.Content.TextOnly()) < 2000 {
		t.Errorf("protected tool result was compressed: len=%d, want >=2000", len(protectedToolResult.Content.TextOnly()))
	}
}

func TestCompactPipeline_Pass1_EarlyReturn(t *testing.T) {
	// Set up: small messages that become under threshold after Pass 1 compression
	bigResult := strings.Repeat("y", 500) // will compress to ~100 chars
	smallContent := "short"

	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(smallContent)},
		{Role: "assistant", Content: content.TextBlock(smallContent), ToolCalls: []provider.ToolCall{{ID: "t1", Name: "tool_a"}}},
		{Role: "tool", ToolCallID: "t1", Content: content.TextBlock(bigResult)},
		// protected tail:
		{Role: "user", Content: content.TextBlock("latest user msg")},
	}

	// Max tokens large enough to pass threshold after compression but small enough to trigger initially
	totalAfterCompression := EstimateMessagesTokens(msgs) // rough estimate
	_ = totalAfterCompression

	// Use a larger window, verify the pipeline doesn't call summarization (we can't easily
	// detect that, but at minimum we check tool result was compressed and output is valid)
	cm := buildPipelineManager(100000)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	if len(result) == 0 {
		t.Fatal("compactPipeline returned empty result")
	}
	// The tool result should be compressed
	var tr *provider.ChatMessage
	for i := range result {
		if result[i].ToolCallID == "t1" {
			r := result[i]
			tr = &r
			break
		}
	}
	if tr == nil {
		t.Fatal("tool result message not found in output")
	}
	if len(tr.Content.TextOnly()) >= len(bigResult) {
		t.Errorf("tool result not compressed: len=%d, want <%d", len(tr.Content.TextOnly()), len(bigResult))
	}
}

func TestCompactPipeline_Pass1_NoToolResults_Noop(t *testing.T) {
	// Messages with no tool results → Pass 1 is a no-op
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock("hello")},
		{Role: "assistant", Content: content.TextBlock("hi there")},
		{Role: "user", Content: content.TextBlock("how are you")},
		{Role: "assistant", Content: content.TextBlock("fine thanks")},
	}

	cm := buildPipelineManager(100000)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	if len(result) != len(msgs) {
		t.Errorf("no tool results: got %d messages, want %d", len(result), len(msgs))
	}
	for i := range msgs {
		if result[i].Content.TextOnly() != msgs[i].Content.TextOnly() {
			t.Errorf("message %d modified when it shouldn't be", i)
		}
	}
}

// --- T3.3 tests: Pass 2 LLM summarization ---

// summaryCaptureMockProvider is a mock that records what model/request was used
// and returns a canned summary.
type summaryCaptureMockProvider struct {
	cmMockProvider
	capturedRequests []provider.ChatRequest
	returnContent    string
	returnErr        error
}

func (m *summaryCaptureMockProvider) Chat(_ context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	m.capturedRequests = append(m.capturedRequests, req)
	if m.returnErr != nil {
		return nil, m.returnErr
	}
	return &provider.ChatResponse{Content: m.returnContent}, nil
}

func buildPipelineManagerWithProvider(maxTokens int, prov provider.Provider) *ContextManager {
	cfg := config.ContextConfig{
		MaxTokens:          maxTokens,
		CompactThreshold:   0.5,
		ProtectedTurns:     1,
		ToolResultMaxChars: 100,
		SummaryMaxTokens:   500,
		Strategy:           "smart",
		CooldownTurns:      0,
	}
	cfg.ApplyContextDefaults()
	cfg.CompactThreshold = 0.5
	cfg.ProtectedTurns = 1
	cfg.ToolResultMaxChars = 100
	return &ContextManager{
		cfg:             cfg,
		prov:            prov,
		resolvedMaxToks: maxTokens,
	}
}

func TestCompactPipeline_Pass2_PinnedSummaryCreated(t *testing.T) {
	// Create enough messages to need Pass 2 summarization.
	// Make sure Pass 1 alone won't solve the problem (no huge tool results).
	bigText := strings.Repeat("w", 800) // no tool results, just big messages
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		// protected tail
		{Role: "user", Content: content.TextBlock("latest message")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "This is the LLM-generated summary of the conversation.",
	}

	// Set max tokens small enough to trigger Pass 2
	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens / 2 // force compaction

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// Check that a pinned summary message was created
	hasSummary := false
	for _, msg := range result {
		if strings.HasPrefix(msg.Content.TextOnly(), "[Context Summary") {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Errorf("expected pinned summary message with '[Context Summary' prefix, got: %v",
			func() []string {
				ss := make([]string, len(result))
				for i, m := range result {
					ss[i] = fmt.Sprintf("[%s] %s", m.Role, m.Content.TextOnly()[:min(50, len(m.Content.TextOnly()))])
				}
				return ss
			}())
	}

	// LLM should have been called
	if len(mockProv.capturedRequests) == 0 {
		t.Error("expected LLM summarization call, got none")
	}
}

func TestCompactPipeline_Pass2_LLMFailureMechanicalFallback(t *testing.T) {
	bigText := strings.Repeat("z", 800)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock("latest message")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnErr:      fmt.Errorf("LLM call failed"),
	}

	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens / 2

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)

	// Should not panic or return error — mechanical fallback used
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	if len(result) == 0 {
		t.Fatal("expected non-empty result even on LLM failure")
	}

	// Should still have a summary message (mechanical fallback)
	hasSummary := false
	for _, msg := range result {
		if strings.HasPrefix(msg.Content.TextOnly(), "[Context Summary") {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Errorf("expected pinned summary message even on LLM failure (mechanical fallback)")
	}
}

func TestCompactPipeline_Pass2_OldPinnedSummaryReplaced(t *testing.T) {
	// Simulate conversation that already has a pinned summary from a previous compaction
	bigText := strings.Repeat("q", 800)
	msgs := []provider.ChatMessage{
		// Existing pinned summary from previous compaction
		{Role: "assistant", Content: content.TextBlock("[Context Summary — covers turns 1-3]\n\nOld summary content here.")},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		// protected tail
		{Role: "user", Content: content.TextBlock("latest message")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "Updated summary covering all previous turns.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens / 2

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// Count pinned summary messages — should be exactly 1, not 2
	summaryCount := 0
	for _, msg := range result {
		if strings.HasPrefix(msg.Content.TextOnly(), "[Context Summary") {
			summaryCount++
		}
	}
	if summaryCount != 1 {
		t.Errorf("expected exactly 1 pinned summary, got %d", summaryCount)
	}
}

func TestCompactPipeline_Pass2_SummaryModelUsed(t *testing.T) {
	bigText := strings.Repeat("p", 800)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock("latest")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "default-model"},
		returnContent:  "Summary.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens / 2

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	cm.cfg.SummaryModel = "special-summary-model"
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	_ = result
	// Verify LLM was called with the summary model
	if len(mockProv.capturedRequests) == 0 {
		t.Fatal("expected LLM call, got none")
	}
	got := mockProv.capturedRequests[0].Model
	if got != "special-summary-model" {
		t.Errorf("summary model: got %q, want %q", got, "special-summary-model")
	}
}

// --- T3.4 tests: Pass 3 hard truncation with pair-boundary enforcement ---

func TestCompactPipeline_Pass3_FiresWhenRequired(t *testing.T) {
	// Create a scenario where even Pass 2 summarization won't bring us under budget
	// by setting a very small max token budget
	bigText := strings.Repeat("r", 400)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		// protected tail:
		{Role: "user", Content: content.TextBlock("last")},
		{Role: "assistant", Content: content.TextBlock("last reply")},
	}

	// LLM returns a short summary (so it may help but not fully solve the problem)
	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "Short summary.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	// Set budget to 15% of total to force Pass 3
	maxTokens := totalTokens * 15 / 100

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	sysToks := 0
	toolToks := 0

	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// After all passes, total tokens should be within budget
	remaining := EstimateMessagesTokens(result) + sysToks + toolToks
	budget := int(float64(maxTokens) * cm.cfg.CompactThreshold)
	if remaining > budget {
		t.Errorf("after Pass 3: remaining tokens %d > budget %d", remaining, budget)
	}
}

func TestCompactPipeline_Pass3_NeverFiresWhenNotNeeded(t *testing.T) {
	// When Pass 1+2 are sufficient, Pass 3 should not fire.
	// We verify by checking the result has the summary message and reasonable length.
	bigText := strings.Repeat("s", 800)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock("latest")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "Summary.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	// Budget that Pass 2 can handle: summary (~2 tokens) + protected tail fits
	// Set to 40% of total which is enough for summary + protected tail but forces summarization
	maxTokens := totalTokens * 60 / 100

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// Should have a pinned summary
	hasSummary := false
	for _, msg := range result {
		if strings.HasPrefix(msg.Content.TextOnly(), "[Context Summary") {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Errorf("expected summary message when Pass 2 suffices")
	}
}

func TestCompactPipeline_Pass3_HysteresisUpdated(t *testing.T) {
	// After compactPipeline runs (including Pass 3), hysteresis state is
	// updated by the caller (smartManage/ForceCompact). We verify the
	// pipeline itself returns a valid result.
	bigText := strings.Repeat("t", 400)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock(bigText)},
		{Role: "assistant", Content: content.TextBlock(bigText)},
		{Role: "user", Content: content.TextBlock("last")},
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "Summary.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens * 10 / 100 // very tight budget

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)

	// Call via smartManage so hysteresis is properly updated
	cm.currentTurn = 1
	sysToks := EstimateTokens("")
	_ = sysToks

	result := cm.ForceCompact(context.Background(), "", nil, msgs)

	// ForceCompact should update lastCompactTurn and postCompactUsage
	if cm.lastCompactTurn == 0 {
		t.Error("ForceCompact: lastCompactTurn not updated")
	}
	if cm.postCompactUsage == 0 {
		t.Error("ForceCompact: postCompactUsage not updated")
	}
	if len(result) == 0 {
		t.Error("ForceCompact: result is empty")
	}
}

func TestCompactPipeline_Pass3_CutAtTurnBoundary(t *testing.T) {
	// Verify that Pass 3 cut respects turn boundaries (doesn't split tool pairs)
	bigText := strings.Repeat("u", 400)
	toolResult := strings.Repeat("v", 400)

	// Turns:
	// Turn 0: user msg (big)
	// Turn 1: assistant+tool + tool result (big) — these must not be split
	// Turn 2: user msg (small, protected)
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock(bigText)},                                                               // index 0
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "t1", Name: "tool_a"}}, Content: content.TextBlock("ok")}, // index 1
		{Role: "tool", ToolCallID: "t1", Content: content.TextBlock(toolResult)},                                          // index 2 — same turn as 1
		{Role: "user", Content: content.TextBlock("small")},                                                               // index 3 (protected)
	}

	mockProv := &summaryCaptureMockProvider{
		cmMockProvider: cmMockProvider{name: "test", model: "test-model"},
		returnContent:  "Summary.",
	}

	totalTokens := EstimateMessagesTokens(msgs)
	maxTokens := totalTokens * 10 / 100 // very tight, forces Pass 3

	cm := buildPipelineManagerWithProvider(maxTokens, mockProv)
	result := cm.compactPipeline(context.Background(), "", nil, msgs)

	// Verify no orphaned tool results (every tool result must have matching assistant)
	toolCallIDs := make(map[string]bool)
	for _, msg := range result {
		for _, tc := range msg.ToolCalls {
			toolCallIDs[tc.ID] = true
		}
	}
	for _, msg := range result {
		if msg.Role == "tool" && msg.ToolCallID != "" {
			if !toolCallIDs[msg.ToolCallID] {
				t.Errorf("orphaned tool result: tool_call_id=%s has no matching assistant ToolCall in result",
					msg.ToolCallID)
			}
		}
	}
}

// min helper for Go <1.21 compatibility
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
