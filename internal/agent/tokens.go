package agent

import (
	"microagent/internal/provider"
)

// EstimateTokens returns approximate token count for a string.
// Uses the heuristic: 1 token ≈ 4 characters for English text.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// EstimateMessageTokens estimates tokens for a ChatMessage.
// Includes role overhead (~4 tokens) + content + tool call data.
func EstimateMessageTokens(msg provider.ChatMessage) int {
	tokens := 4 // role + formatting overhead
	tokens += EstimateTokens(msg.Content)
	for _, tc := range msg.ToolCalls {
		tokens += EstimateTokens(tc.Name) + EstimateTokens(string(tc.Input))
	}
	if msg.ToolCallID != "" {
		tokens += EstimateTokens(msg.ToolCallID)
	}
	return tokens
}

// EstimateMessagesTokens estimates total tokens for a slice of messages.
func EstimateMessagesTokens(msgs []provider.ChatMessage) int {
	total := 0
	for _, msg := range msgs {
		total += EstimateMessageTokens(msg)
	}
	return total
}
