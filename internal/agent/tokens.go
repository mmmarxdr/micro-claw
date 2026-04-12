package agent

import (
	"microagent/internal/content"
	"microagent/internal/provider"
)

// AnthropicImageTokens is the per-image token estimate used by the context
// builder for Anthropic requests.
// ~1500 tokens per image per Anthropic docs
// (https://docs.anthropic.com/en/docs/build-with-claude/vision#image-costs)
const AnthropicImageTokens = 1500

// OpenAIImageTokens is the baseline per-image token estimate for OpenAI vision.
// baseline 85 tokens + variable tiles per
// https://platform.openai.com/docs/guides/vision/calculating-costs
const OpenAIImageTokens = 85

// GeminiImageTokens is the per-image token estimate for Gemini multimodal requests.
// 258 tokens per image per Gemini docs.
const GeminiImageTokens = 258

// providerImageTokens maps canonical lowercase provider names to their per-image
// token estimates. Unknown providers fall back to AnthropicImageTokens (safest
// upper bound — better to over-estimate than to blow the context budget).
var providerImageTokens = map[string]int{
	"anthropic":  AnthropicImageTokens,
	"openai":     OpenAIImageTokens,
	"gemini":     GeminiImageTokens,
	"openrouter": AnthropicImageTokens, // OpenRouter routes to Claude by default
	"ollama":     0,                    // Ollama is text-only; degradation prevents images from reaching it
}

// imageTokensFor returns the per-image token constant for the named provider.
// providerName must be lowercase (matches the canonical Name() return values).
func imageTokensFor(providerName string) int {
	if v, ok := providerImageTokens[providerName]; ok {
		return v
	}
	return AnthropicImageTokens // safe upper-bound default
}

// EstimateTokens returns approximate token count for a string.
// Uses the heuristic: 1 token ≈ 4 characters for English text.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// EstimateMessageTokens estimates tokens for a ChatMessage using the Anthropic
// image token constant as the default. Delegates to EstimateMessageTokensFor.
// Preserved for backward compatibility — callers that do not have a provider
// name (e.g. summarize.go) continue to use this.
func EstimateMessageTokens(msg provider.ChatMessage) int {
	return EstimateMessageTokensFor(msg, "")
}

// EstimateMessageTokensFor estimates tokens for a ChatMessage with provider
// awareness. It iterates over content blocks:
//   - BlockText: EstimateTokens(b.Text)
//   - BlockImage: per-provider image token constant (imageTokensFor)
//   - BlockAudio: 500 tokens (rough estimate; no provider-specific docs yet)
//   - BlockDocument: 200 tokens (rough estimate for metadata overhead)
//
// providerName must be the canonical lowercase Name() string (e.g. "anthropic",
// "openai", "gemini", "openrouter"). An empty or unknown name falls back to
// AnthropicImageTokens for images.
func EstimateMessageTokensFor(msg provider.ChatMessage, providerName string) int {
	imgTokens := imageTokensFor(providerName)

	tokens := 4 // role + formatting overhead
	for _, b := range msg.Content {
		switch b.Type {
		case content.BlockText:
			tokens += EstimateTokens(b.Text)
		case content.BlockImage:
			tokens += imgTokens
		case content.BlockAudio:
			tokens += 500 // rough estimate; no provider-specific docs yet
		case content.BlockDocument:
			tokens += 200 // rough estimate for metadata overhead
		}
	}
	for _, tc := range msg.ToolCalls {
		tokens += EstimateTokens(tc.Name) + EstimateTokens(string(tc.Input))
	}
	if msg.ToolCallID != "" {
		tokens += EstimateTokens(msg.ToolCallID)
	}
	return tokens
}

// EstimateMessagesTokens estimates total tokens for a slice of messages using
// the default (Anthropic) image token constant. Preserved for backward compat.
func EstimateMessagesTokens(msgs []provider.ChatMessage) int {
	return EstimateMessagesTokensFor(msgs, "")
}

// EstimateMessagesTokensFor estimates total tokens for a slice of messages with
// provider awareness. providerName is the canonical lowercase Name() string.
func EstimateMessagesTokensFor(msgs []provider.ChatMessage, providerName string) int {
	total := 0
	for _, msg := range msgs {
		total += EstimateMessageTokensFor(msg, providerName)
	}
	return total
}
