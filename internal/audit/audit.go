package audit

import (
	"context"
	"time"
)

// AuditEvent captures a single observable agent action for metrics and audit trails.
type AuditEvent struct {
	ID         string    `json:"id"`
	ScopeID    string    `json:"scope_id"`
	EventType  string    `json:"event_type"` // "llm_call" | "tool_use"
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"duration_ms"`

	// LLM call fields (EventType == "llm_call")
	Model        string `json:"model,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	Iteration    int    `json:"iteration,omitempty"`

	// Tool execution fields (EventType == "tool_use")
	ToolName string            `json:"tool_name,omitempty"`
	ToolOK   bool              `json:"tool_ok,omitempty"`
	Details  map[string]string `json:"details,omitempty"` // url, status_code, command, exit_code, etc.
}

// Auditor records audit events. Implementations must be safe for concurrent use.
type Auditor interface {
	Emit(ctx context.Context, event AuditEvent) error
	Close() error
}

// NoopAuditor discards all events. Used when audit is disabled or in tests.
type NoopAuditor struct{}

func (NoopAuditor) Emit(_ context.Context, _ AuditEvent) error { return nil }
func (NoopAuditor) Close() error                               { return nil }
