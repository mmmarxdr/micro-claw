package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"daimon/internal/content"
	"daimon/internal/provider"
)

// findTurnBoundaries returns the indices at which a new "turn" starts.
//
// Rules:
//   - Index 0 is always a boundary (start of conversation).
//   - A new boundary occurs when the role changes from the previous message.
//   - EXCEPTION: an assistant message with ToolCalls and the immediately
//     following tool result messages are treated as ONE turn. The boundary
//     is at the assistant message; the tool results do NOT start new boundaries.
//
// Tool result grouping algorithm:
//  1. Walk the slice maintaining a set of "pending" tool-call IDs.
//  2. When we encounter an assistant message with ToolCalls, open a group:
//     record all tool-call IDs as pending.
//  3. When we encounter a tool message whose ToolCallID is in the pending set,
//     consume it (remove from pending) — it belongs to the current group.
//  4. When pending is empty (all tool results received), the group is closed.
//  5. Any other role transition that is NOT consuming a pending tool result
//     starts a new boundary.
func findTurnBoundaries(messages []provider.ChatMessage) []int {
	if len(messages) == 0 {
		return nil
	}

	boundaries := []int{0} // first message always starts a turn
	pendingToolCallIDs := make(map[string]bool)

	for i := 1; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == "tool" && pendingToolCallIDs[msg.ToolCallID] {
			// Consume this tool result — part of the current turn
			delete(pendingToolCallIDs, msg.ToolCallID)
			continue
		}

		// This message starts a new turn.
		boundaries = append(boundaries, i)

		// If it's an assistant with tool calls, register its IDs as pending.
		pendingToolCallIDs = make(map[string]bool) // reset pending for new group
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					pendingToolCallIDs[tc.ID] = true
				}
			}
		}
	}

	return boundaries
}

// findNearestBoundaryBefore returns the largest boundary value that is ≤ idx.
// If no boundary satisfies this, returns 0.
func findNearestBoundaryBefore(idx int, boundaries []int) int {
	result := 0
	for _, b := range boundaries {
		if b <= idx {
			result = b
		}
	}
	return result
}

// compactPipeline implements the three-pass compaction pipeline.
//
// Pass 1: Compress tool results outside the protected tail.
// Pass 2: LLM summarization (or mechanical fallback) of non-protected content.
// Pass 3: Hard truncation at a turn boundary as a last resort.
//
// Each pass short-circuits if the token budget is satisfied after it completes.
func (cm *ContextManager) compactPipeline(
	ctx context.Context,
	systemPrompt string,
	toolDefs []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	sysToks := EstimateTokens(systemPrompt)
	toolToks := estimateToolDefTokens(toolDefs)
	overhead := sysToks + toolToks

	budget := int(float64(cm.resolvedMaxToks)*cm.cfg.CompactThreshold) - overhead
	if budget < 0 {
		budget = 0
	}

	// Compute turn boundaries
	boundaries := findTurnBoundaries(messages)

	// protectedStart: index of the first message that belongs to the
	// protected tail (last ProtectedTurns turns must not be touched).
	protectedStart := computeProtectedStart(boundaries, cm.cfg.ProtectedTurns, len(messages))

	// --- Pass 1: Compress tool results before protectedStart ---
	msgs := make([]provider.ChatMessage, len(messages))
	copy(msgs, messages)

	for i := 0; i < protectedStart; i++ {
		if msgs[i].Role == "tool" {
			msgs[i] = compressToolResult(msgs[i], cm.cfg.ToolResultMaxChars)
		}
	}

	if EstimateMessagesTokens(msgs) <= budget {
		return msgs
	}

	// --- Pass 2: LLM summarization with mechanical fallback ---
	msgs = cm.pass2Summarize(ctx, msgs, protectedStart, budget)
	if EstimateMessagesTokens(msgs) <= budget {
		return msgs
	}

	// --- Pass 3: Hard truncation at turn boundary ---
	beforeToks := EstimateMessagesTokens(msgs)
	msgs = cm.pass3HardTruncate(msgs, budget, boundaries)
	afterToks := EstimateMessagesTokens(msgs)

	slog.Warn("context hard-truncated (Pass 3)",
		"before_tokens", beforeToks,
		"after_tokens", afterToks,
		"budget", budget,
	)

	return msgs
}

// computeProtectedStart returns the index of the first message that belongs
// to the protected tail. The protected tail is the last `protectedTurns`
// complete turns as identified by boundaries.
//
// If protectedTurns >= number of turns, returns 0 (everything is protected).
func computeProtectedStart(boundaries []int, protectedTurns int, msgCount int) int {
	if len(boundaries) == 0 || protectedTurns <= 0 {
		return msgCount
	}
	if protectedTurns >= len(boundaries) {
		return 0
	}
	// The protected tail starts at boundaries[len(boundaries)-protectedTurns]
	return boundaries[len(boundaries)-protectedTurns]
}

// pass2Summarize implements Pass 2: remove (and summarize) messages before
// protectedStart, replacing them with a single pinned summary message.
func (cm *ContextManager) pass2Summarize(
	ctx context.Context,
	msgs []provider.ChatMessage,
	protectedStart int,
	budget int,
) []provider.ChatMessage {
	// Find existing pinned summary (if any) — it starts with "[Context Summary"
	existingSummaryIdx := -1
	for i, msg := range msgs {
		if strings.HasPrefix(msg.Content.TextOnly(), "[Context Summary") {
			existingSummaryIdx = i
			break
		}
	}

	// Determine the slice to summarize: from after existing summary (or 0) to protectedStart
	summarizeFrom := 0
	if existingSummaryIdx >= 0 {
		summarizeFrom = existingSummaryIdx + 1
	}

	if summarizeFrom >= protectedStart {
		// Nothing to summarize
		return msgs
	}

	toSummarize := msgs[summarizeFrom:protectedStart]
	protected := msgs[protectedStart:]

	// Call LLM summarization; fall back to mechanical on error
	summaryText, err := cm.summarizeMessages(ctx, toSummarize)
	if err != nil {
		slog.Warn("LLM summarization failed, using mechanical fallback", "error", err)
		summaryText = mechanicalSummary(toSummarize)
	}

	// Build the turn range label
	turnLabel := fmt.Sprintf("[Context Summary — covers turns 1-%d]", protectedStart)
	pinnedMsg := provider.ChatMessage{
		Role:    "assistant",
		Content: content.TextBlock(turnLabel + "\n\n" + summaryText),
	}

	// Assemble result: any content before summarizeFrom + pinned + protected
	var result []provider.ChatMessage
	if summarizeFrom > 0 {
		// There was an existing summary before summarizeFrom; skip old summary (existingSummaryIdx),
		// preserve anything between 0 and existingSummaryIdx (there shouldn't be any, but be safe)
		// Actually: if existingSummaryIdx >= 0, summarizeFrom = existingSummaryIdx+1
		// We want: nothing before the old summary (drop it), then pinned, then protected
		_ = existingSummaryIdx // old summary is dropped by not including msgs[0:summarizeFrom-1 range]
	}
	result = append(result, pinnedMsg)
	result = append(result, protected...)

	return result
}

// summarizeMessages calls the provider LLM to produce a summary of the given messages.
// Uses cfg.SummaryModel as the model override if set.
func (cm *ContextManager) summarizeMessages(ctx context.Context, messages []provider.ChatMessage) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	summaryMaxToks := cm.cfg.SummaryMaxTokens
	if summaryMaxToks <= 0 {
		summaryMaxToks = 1000
	}

	prompt := buildSummarizationPrompt(messages, summaryMaxToks)

	summarizeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req := provider.ChatRequest{
		SystemPrompt: "You are a conversation summarizer. Produce concise, structured summaries that preserve key decisions, file paths, tool results, and action items. Never fabricate information not present in the input.",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock(prompt)},
		},
		MaxTokens:   summaryMaxToks,
		Temperature: 0.0,
	}

	// Apply summary model override if configured
	if cm.cfg.SummaryModel != "" {
		req.Model = cm.cfg.SummaryModel
	}

	resp, err := cm.prov.Chat(summarizeCtx, req)
	if err != nil {
		return "", fmt.Errorf("summarization LLM call failed: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("summarization returned empty response")
	}

	return resp.Content, nil
}

// pass3HardTruncate implements Pass 3: walk backward accumulating token counts
// until the budget is met. The cut point is snapped to the nearest turn boundary
// to avoid splitting tool call groups. The pinned summary (if present) is always
// kept at the front.
func (cm *ContextManager) pass3HardTruncate(
	msgs []provider.ChatMessage,
	budget int,
	originalBoundaries []int,
) []provider.ChatMessage {
	if len(msgs) == 0 {
		return msgs
	}

	// Recompute boundaries for the (post-Pass-2) message slice
	boundaries := findTurnBoundaries(msgs)

	// Walk backward, accumulating tokens
	usedTokens := 0
	cutIdx := len(msgs) // start with nothing discarded

	for i := len(msgs) - 1; i >= 0; i-- {
		tok := EstimateMessageTokens(msgs[i])
		if usedTokens+tok > budget {
			// Can't include msgs[i] — snap cut to nearest turn boundary
			snap := findNearestBoundaryBefore(i, boundaries)
			cutIdx = snap
			break
		}
		usedTokens += tok
		cutIdx = i
	}

	// If we kept nothing or cut at 0, keep at least the pinned summary + protected tail
	if cutIdx >= len(msgs) {
		// All messages fit; nothing to truncate
		return msgs
	}

	// Check if there's a pinned summary at the front
	hasPinned := len(msgs) > 0 && strings.HasPrefix(msgs[0].Content.TextOnly(), "[Context Summary")

	result := msgs[cutIdx:]
	if hasPinned && cutIdx > 0 {
		// Prepend the pinned summary so it's never lost
		result = append([]provider.ChatMessage{msgs[0]}, result...)
	}

	return result
}
