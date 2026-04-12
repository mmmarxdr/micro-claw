package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"microagent/internal/content"
	"microagent/internal/provider"
)

// buildSummarizationPrompt creates a structured prompt for LLM-based history summarization.
func buildSummarizationPrompt(messages []provider.ChatMessage, maxTokens int) string {
	var sb strings.Builder
	sb.WriteString("Summarize this conversation segment concisely. For each exchange:\n")
	sb.WriteString("- What the user asked or requested\n")
	sb.WriteString("- What tools were used and their key results (not full output)\n")
	sb.WriteString("- What was decided or concluded\n")
	sb.WriteString("- Any errors or issues encountered\n\n")
	sb.WriteString("Keep technical details (file paths, function names, error messages) but remove verbose output.\n")
	sb.WriteString(fmt.Sprintf("Format as a brief narrative, max %d tokens.\n\n", maxTokens))
	sb.WriteString("--- CONVERSATION SEGMENT ---\n\n")

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			sb.WriteString("[User]: ")
			sb.WriteString(truncateContent(msg.Content.TextOnly(), 500))
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("[Assistant]: ")
			if len(msg.ToolCalls) > 0 {
				names := make([]string, len(msg.ToolCalls))
				for i, tc := range msg.ToolCalls {
					names[i] = tc.Name
				}
				sb.WriteString(fmt.Sprintf("(called tools: %s) ", strings.Join(names, ", ")))
			}
			sb.WriteString(truncateContent(msg.Content.TextOnly(), 300))
			sb.WriteString("\n\n")
		case "tool":
			sb.WriteString(fmt.Sprintf("[Tool Result (id=%s)]: ", msg.ToolCallID))
			sb.WriteString(truncateContent(msg.Content.TextOnly(), 200))
			sb.WriteString("\n\n")
		}
	}

	return sb.String()
}

// summarizeHistory uses the LLM to produce a structured summary of messages to be compressed.
func (a *Agent) summarizeHistory(ctx context.Context, messages []provider.ChatMessage) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	summaryTokens := a.config.SummaryTokens
	if summaryTokens <= 0 {
		summaryTokens = 1000
	}

	prompt := buildSummarizationPrompt(messages, summaryTokens)

	summarizeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := a.provider.Chat(summarizeCtx, provider.ChatRequest{
		SystemPrompt: "You are a conversation summarizer. Produce concise, structured summaries that preserve key decisions, file paths, tool results, and action items. Never fabricate information not present in the input.",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock(prompt)},
		},
		MaxTokens:   summaryTokens,
		Temperature: 0.0,
	})
	if err != nil {
		return "", fmt.Errorf("summarization LLM call failed: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("summarization returned empty response")
	}

	return resp.Content, nil
}

// mechanicalSummary produces a fallback summary without LLM, extracting user messages and tool names.
func mechanicalSummary(messages []provider.ChatMessage) string {
	var sb strings.Builder
	sb.WriteString("Previous conversation summary (mechanical):\n")

	toolNames := make(map[string]bool)
	userMsgCount := 0

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			userMsgCount++
			sb.WriteString(fmt.Sprintf("- User (%d): %s\n", userMsgCount, truncateContent(msg.Content.TextOnly(), 100)))
		case "assistant":
			for _, tc := range msg.ToolCalls {
				toolNames[tc.Name] = true
			}
		}
	}

	if len(toolNames) > 0 {
		names := make([]string, 0, len(toolNames))
		for name := range toolNames {
			names = append(names, name)
		}
		sb.WriteString(fmt.Sprintf("- Tools used: %s\n", strings.Join(names, ", ")))
	}

	return sb.String()
}

// compressToolResult reduces a tool result message for history storage.
// Keeps first and last characters plus metadata, removes bulk output.
// Tool results are expected to be text-only; if the content has media blocks,
// it is returned unchanged (media blocks in tool results are unexpected).
func compressToolResult(msg provider.ChatMessage, maxChars int) provider.ChatMessage {
	// Compress against the text-only representation.
	text := msg.Content.TextOnly()
	if len(text) <= maxChars {
		return msg
	}

	headSize := 500
	tailSize := 200
	compressed := msg
	if maxChars < headSize+tailSize+50 {
		// For very small maxChars, just truncate.
		compressed.Content = content.TextBlock(
			text[:maxChars] + fmt.Sprintf("\n[...truncated %d chars...]", len(text)-maxChars),
		)
		return compressed
	}

	compressed.Content = content.TextBlock(
		text[:headSize] +
			fmt.Sprintf("\n[...truncated %d chars...]\n", len(text)-headSize-tailSize) +
			text[len(text)-tailSize:],
	)
	return compressed
}

// truncateContent trims a string to maxLen characters, appending "..." if truncated.
func truncateContent(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// manageContextTokens handles the token-aware context management.
// It applies a three-pass approach to reduce context size within the token budget:
//  1. Compress tool results older than the last 5 messages
//  2. Summarize oldest messages into a summary block
//  3. Hard truncate oldest messages as a last resort
//
// Returns the (potentially modified) messages and any summary prepended.
func (a *Agent) manageContextTokens(ctx context.Context, systemPrompt string, messages []provider.ChatMessage) []provider.ChatMessage {
	maxTokens := a.config.MaxContextTokens
	if maxTokens <= 0 {
		return messages
	}

	provName := a.provider.Name()
	systemTokens := EstimateTokens(systemPrompt)
	msgTokens := EstimateMessagesTokensFor(messages, provName)
	totalTokens := systemTokens + msgTokens

	if totalTokens <= maxTokens {
		slog.Debug("context within token budget", "total_tokens", totalTokens, "budget", maxTokens)
		return messages
	}

	slog.Info("context exceeds token budget, starting compression",
		"total_tokens", totalTokens, "budget", maxTokens, "messages", len(messages))

	// === Pass 1: Compress tool results older than last 5 messages ===
	protectedTail := 5
	if protectedTail > len(messages) {
		protectedTail = len(messages)
	}
	cutoff := len(messages) - protectedTail

	for i := 0; i < cutoff; i++ {
		if messages[i].Role == "tool" {
			messages[i] = compressToolResult(messages[i], 800)
		}
	}

	msgTokens = EstimateMessagesTokensFor(messages, provName)
	totalTokens = systemTokens + msgTokens
	if totalTokens <= maxTokens {
		slog.Info("context within budget after tool compression", "total_tokens", totalTokens)
		return messages
	}

	// === Pass 2: Summarize oldest messages ===
	// Find how many messages we need to summarize to fit the budget.
	// We want to keep at least the last protectedTail messages plus room for a summary.
	summaryBudget := a.config.SummaryTokens
	if summaryBudget <= 0 {
		summaryBudget = 1000
	}

	// Calculate how many tokens we need to free
	tokensToFree := totalTokens - maxTokens + summaryBudget + 50 // +50 buffer for summary message overhead

	// Find the oldest N messages whose tokens sum >= tokensToFree
	summarizeEnd := 0
	freedTokens := 0
	for i := 0; i < cutoff && freedTokens < tokensToFree; i++ {
		freedTokens += EstimateMessageTokensFor(messages[i], provName)
		summarizeEnd = i + 1
	}

	if summarizeEnd > 0 {
		toSummarize := messages[:summarizeEnd]
		remaining := messages[summarizeEnd:]

		summaryText, err := a.summarizeHistory(ctx, toSummarize)
		if err != nil {
			slog.Warn("LLM summarization failed, using mechanical fallback", "error", err)
			summaryText = mechanicalSummary(toSummarize)
		}

		summaryMsg := provider.ChatMessage{
			Role:    "assistant",
			Content: content.TextBlock("(Summary of previous conversation):\n" + summaryText),
		}
		messages = append([]provider.ChatMessage{summaryMsg}, remaining...)

		msgTokens = EstimateMessagesTokensFor(messages, provName)
		totalTokens = systemTokens + msgTokens
		slog.Info("context after summarization",
			"total_tokens", totalTokens, "summarized_messages", summarizeEnd, "remaining_messages", len(messages))

		if totalTokens <= maxTokens {
			return messages
		}
	}

	// === Pass 3: Hard truncate — keep only messages that fit ===
	// Always keep: first message (summary if present) + last message (latest user)
	// Fill from the end backward.
	slog.Warn("context still over budget after summarization, hard truncating",
		"total_tokens", totalTokens, "budget", maxTokens)

	budgetForMessages := maxTokens - systemTokens
	if budgetForMessages < 0 {
		budgetForMessages = 0
	}

	// Work backward: keep as many recent messages as fit
	kept := make([]provider.ChatMessage, 0, len(messages))
	usedTokens := 0

	for i := len(messages) - 1; i >= 0; i-- {
		msgTok := EstimateMessageTokensFor(messages[i], provName)
		if usedTokens+msgTok > budgetForMessages {
			break
		}
		kept = append(kept, messages[i])
		usedTokens += msgTok
	}

	// Reverse to restore original order
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	slog.Info("hard truncation complete",
		"kept_messages", len(kept), "dropped_messages", len(messages)-len(kept),
		"tokens_saved", totalTokens-systemTokens-usedTokens)

	return kept
}
