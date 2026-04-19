package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"daimon/internal/content"
)

// Sentinel errors for provider failure classification.
// Use errors.Is() to check for these in callers.
var (
	ErrRateLimit   = errors.New("rate limit")            // HTTP 429
	ErrUnavailable = errors.New("service unavailable")   // HTTP 5xx, network/timeout
	ErrAuth        = errors.New("authentication failed") // HTTP 401, 403
	ErrBadRequest  = errors.New("bad request")           // HTTP 400, other 4xx
)

// wrapNetworkError classifies a network-level error using sentinel errors.
// context.Canceled and context.DeadlineExceeded are returned as-is (caller cancelled).
// All other network errors are wrapped as ErrUnavailable.
func wrapNetworkError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

type ChatMessage struct {
	Role       string         `json:"role"` // "user", "assistant", "tool"
	Content    content.Blocks `json:"content"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// UnmarshalJSON accepts both the legacy plain-string form and the new
// array-of-blocks form for the "content" field.
//
//	"content":"hello"   → Blocks{{Type:text, Text:"hello"}}
//	"content":[{…}]    → unmarshal directly as []ContentBlock
//	"content":null/""  → nil Blocks
//
// No custom MarshalJSON — default struct marshal writes the new array form,
// which is write-forward. Old binaries with the Phase 0 forward-compat shim
// can still read the array form.
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID

	bs, err := content.UnmarshalBlocks(raw.Content)
	if err != nil {
		return fmt.Errorf("ChatMessage.Content: %w", err)
	}
	m.Content = bs
	return nil
}

type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ChatRequest struct {
	Model        string // optional per-request model override; empty = use provider default
	SystemPrompt string
	Messages     []ChatMessage
	Tools        []ToolDefinition
	MaxTokens    int
	Temperature  float64
}

type ChatResponse struct {
	Content    string     // text content (may be empty if only tool calls)
	ToolCalls  []ToolCall // tool calls to execute (may be empty if only text)
	Usage      UsageStats
	StopReason string // "end_turn", "tool_use", "max_tokens"
}

type UsageStats struct {
	InputTokens  int
	OutputTokens int
}

type Provider interface {
	Name() string
	Model() string // returns the model ID used for API calls (e.g. "anthropic/claude-haiku-4-5")
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	SupportsTools() bool
	SupportsMultimodal() bool
	SupportsAudio() bool
	HealthCheck(ctx context.Context) (string, error)
}

// EmbeddingProvider is an optional interface for providers that support
// text embedding generation. Callers type-assert: ep, ok := prov.(EmbeddingProvider)
type EmbeddingProvider interface {
	// Embed generates a dense vector embedding for the given text.
	// The returned slice length depends on the provider's model.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ModelInfo describes a model available from a provider.
type ModelInfo struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ContextLength  int      `json:"context_length"`
	PromptCost     float64  `json:"prompt_cost"`              // USD per 1M tokens
	CompletionCost float64  `json:"completion_cost"`          // USD per 1M tokens
	Free           bool     `json:"free"`
	SupportedParameters []string `json:"supported_parameters,omitempty"` // e.g. ["reasoning"]
}

// ModelLister is an optional interface for providers that can enumerate
// available models. Callers type-assert: ml, ok := prov.(ModelLister)
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
