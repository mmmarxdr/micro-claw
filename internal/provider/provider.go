package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Role       string     `json:"role"` // "user", "assistant", "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
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
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	SupportsTools() bool
	HealthCheck(ctx context.Context) (string, error)
}

// EmbeddingProvider is an optional interface for providers that support
// text embedding generation. Callers type-assert: ep, ok := prov.(EmbeddingProvider)
type EmbeddingProvider interface {
	// Embed generates a dense vector embedding for the given text.
	// The returned slice length depends on the provider's model.
	Embed(ctx context.Context, text string) ([]float32, error)
}
