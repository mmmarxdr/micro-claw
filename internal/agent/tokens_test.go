package agent

import (
	"encoding/json"
	"testing"

	"daimon/internal/content"
	"daimon/internal/provider"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"single char", "a", 1},
		{"four chars", "abcd", 1},
		{"five chars", "abcde", 2},
		{"eight chars", "abcdefgh", 2},
		{"twelve chars", "abcdefghijkl", 3},
		{"hello world", "hello world", 3},                                 // 11 chars → (11+3)/4 = 3
		{"long text", "The quick brown fox jumps over the lazy dog.", 11}, // 44 chars → (44+3)/4 = 47/4 = 11
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.input)
			if got != tt.expected {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestEstimateMessageTokens_UserMessage(t *testing.T) {
	msg := provider.ChatMessage{
		Role:    "user",
		Content: content.TextBlock("Hello, how are you?"),
	}
	tokens := EstimateMessageTokens(msg)
	// 4 (overhead) + (19+3)/4 = 4 + 5 = 9
	expected := 4 + EstimateTokens("Hello, how are you?")
	if tokens != expected {
		t.Errorf("got %d, want %d", tokens, expected)
	}
}

func TestEstimateMessageTokens_AssistantWithToolCalls(t *testing.T) {
	msg := provider.ChatMessage{
		Role:    "assistant",
		Content: content.TextBlock("Let me check that."),
		ToolCalls: []provider.ToolCall{
			{
				ID:    "tc1",
				Name:  "shell",
				Input: json.RawMessage(`{"command": "ls -la"}`),
			},
		},
	}
	tokens := EstimateMessageTokens(msg)
	expected := 4 +
		EstimateTokens("Let me check that.") +
		EstimateTokens("shell") +
		EstimateTokens(`{"command": "ls -la"}`)
	if tokens != expected {
		t.Errorf("got %d, want %d", tokens, expected)
	}
}

func TestEstimateMessageTokens_ToolResult(t *testing.T) {
	msg := provider.ChatMessage{
		Role:       "tool",
		Content:    content.TextBlock("file1.txt\nfile2.txt\nfile3.txt"),
		ToolCallID: "tc1",
	}
	tokens := EstimateMessageTokens(msg)
	expected := 4 +
		EstimateTokens("file1.txt\nfile2.txt\nfile3.txt") +
		EstimateTokens("tc1")
	if tokens != expected {
		t.Errorf("got %d, want %d", tokens, expected)
	}
}

func TestEstimateMessageTokens_EmptyMessage(t *testing.T) {
	msg := provider.ChatMessage{Role: "user"}
	tokens := EstimateMessageTokens(msg)
	if tokens != 4 {
		t.Errorf("got %d, want 4 (overhead only)", tokens)
	}
}

func TestEstimateMessagesTokens(t *testing.T) {
	msgs := []provider.ChatMessage{
		{Role: "user", Content: content.TextBlock("hello")},
		{Role: "assistant", Content: content.TextBlock("hi there")},
	}
	total := EstimateMessagesTokens(msgs)
	expected := EstimateMessageTokens(msgs[0]) + EstimateMessageTokens(msgs[1])
	if total != expected {
		t.Errorf("got %d, want %d", total, expected)
	}
}

func TestEstimateMessagesTokens_Empty(t *testing.T) {
	total := EstimateMessagesTokens(nil)
	if total != 0 {
		t.Errorf("got %d, want 0", total)
	}
}
