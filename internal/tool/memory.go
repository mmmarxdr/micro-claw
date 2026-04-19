package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"daimon/internal/store"
)

// scopeKey is the unexported context key type for memory scope.
type scopeKey struct{}

// WithScope returns a new context carrying the given memory scope string.
// The scope is typically "channelID:senderID" and is consumed by the memory tools.
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFromContext extracts the memory scope from ctx.
// Returns "" if no scope was set.
func ScopeFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(scopeKey{}).(string); ok {
		return v
	}
	return ""
}

// MemoryToolDeps holds the dependencies for the memory tool set.
// Using callback functions avoids import cycles between internal/tool and internal/agent.
type MemoryToolDeps struct {
	Store         store.Store
	EnqueueEnrich func(entry store.MemoryEntry)    // nil if enricher disabled
	EnqueueEmbed  func(id, scope, content string)  // nil if embedding disabled
}

// BuildMemoryTools constructs the four memory tools and returns them keyed by name.
// The returned map is ready for MergeTools.
func BuildMemoryTools(deps MemoryToolDeps) map[string]Tool {
	m := make(map[string]Tool)

	smt := &saveMemoryTool{deps: deps}
	m[smt.Name()] = smt

	srmt := &searchMemoryTool{deps: deps}
	m[srmt.Name()] = srmt

	umt := &updateMemoryTool{deps: deps}
	m[umt.Name()] = umt

	fmt := &forgetMemoryTool{deps: deps}
	m[fmt.Name()] = fmt

	return m
}

// truncateTitle returns the first n bytes (rune-safe truncation) of s.
func truncateTitle(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// truncateContent returns up to n runes of s, appending "…" if truncated.
func truncateContent(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// ---------------------------------------------------------------------------
// save_memory tool
// ---------------------------------------------------------------------------

type saveMemoryTool struct {
	deps MemoryToolDeps
}

func (t *saveMemoryTool) Name() string { return "save_memory" }

func (t *saveMemoryTool) Description() string {
	return "Save an important fact, preference, or decision to long-term memory. Use when the user tells you something worth remembering."
}

func (t *saveMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["content"],
  "properties": {
    "content": {
      "type": "string",
      "description": "The information to remember"
    },
    "topic": {
      "type": "string",
      "description": "Optional topic or category for this memory"
    },
    "type": {
      "type": "string",
      "description": "Memory type",
      "enum": ["fact", "preference", "instruction", "decision", "context"]
    }
  }
}`)
}

type saveMemoryParams struct {
	Content string `json:"content"`
	Topic   string `json:"topic,omitempty"`
	Type    string `json:"type,omitempty"`
}

func (t *saveMemoryTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input saveMemoryParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.Content) == "" {
		return ToolResult{IsError: true, Content: "content cannot be empty"}, nil
	}

	scope := ScopeFromContext(ctx)
	title := truncateTitle(input.Content, 80)
	now := time.Now().UTC()

	entry := store.MemoryEntry{
		ID:         uuid.New().String(),
		ScopeID:    scope,
		Topic:      input.Topic,
		Type:       input.Type,
		Title:      title,
		Content:    input.Content,
		Source:     "tool:save_memory",
		Importance: 7,
		CreatedAt:  now,
	}

	if err := t.deps.Store.AppendMemory(ctx, scope, entry); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to save memory: %v", err)}, nil
	}

	if t.deps.EnqueueEnrich != nil {
		t.deps.EnqueueEnrich(entry)
	}
	if t.deps.EnqueueEmbed != nil {
		t.deps.EnqueueEmbed(entry.ID, scope, entry.Content)
	}

	return ToolResult{Content: fmt.Sprintf("Memory saved: %s", title)}, nil
}

// ---------------------------------------------------------------------------
// search_memory tool
// ---------------------------------------------------------------------------

type searchMemoryTool struct {
	deps MemoryToolDeps
}

func (t *searchMemoryTool) Name() string { return "search_memory" }

func (t *searchMemoryTool) Description() string {
	return "Search your long-term memory for facts, preferences, or past context. Use to recall information from previous conversations."
}

func (t *searchMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "Search terms or question to look up in memory"
    },
    "topic": {
      "type": "string",
      "description": "Optional topic filter to narrow results"
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of results to return (default 5, max 20)",
      "minimum": 1,
      "maximum": 20
    }
  }
}`)
}

type searchMemoryParams struct {
	Query string `json:"query"`
	Topic string `json:"topic,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func (t *searchMemoryTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input searchMemoryParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.Query) == "" {
		return ToolResult{IsError: true, Content: "query cannot be empty"}, nil
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	// Append topic to query to improve search relevance when topic is set.
	query := input.Query
	if input.Topic != "" {
		query = query + " " + input.Topic
	}

	scope := ScopeFromContext(ctx)
	entries, err := t.deps.Store.SearchMemory(ctx, scope, query, limit)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(entries) == 0 {
		return ToolResult{Content: "No memories found."}, nil
	}

	var sb strings.Builder
	for i, e := range entries {
		typeStr := e.Type
		if typeStr == "" {
			typeStr = "memory"
		}
		topicStr := ""
		if e.Topic != "" {
			topicStr = "[" + e.Topic + "] "
		}
		preview := truncateContent(e.Content, 200)
		fmt.Fprintf(&sb, "%d. [%s] %s%s — %s (id: %s)\n",
			i+1, typeStr, topicStr, e.Title, preview, e.ID)
	}

	return ToolResult{Content: strings.TrimRight(sb.String(), "\n")}, nil
}

// ---------------------------------------------------------------------------
// update_memory tool
// ---------------------------------------------------------------------------

type updateMemoryTool struct {
	deps MemoryToolDeps
}

func (t *updateMemoryTool) Name() string { return "update_memory" }

func (t *updateMemoryTool) Description() string {
	return "Update an existing memory with new or corrected information. Use the memory ID from search_memory results."
}

func (t *updateMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["id", "content"],
  "properties": {
    "id": {
      "type": "string",
      "description": "The memory ID to update (from search_memory results)"
    },
    "content": {
      "type": "string",
      "description": "The new or corrected content for this memory"
    }
  }
}`)
}

type updateMemoryParams struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func (t *updateMemoryTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input updateMemoryParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.ID) == "" {
		return ToolResult{IsError: true, Content: "id cannot be empty"}, nil
	}
	if strings.TrimSpace(input.Content) == "" {
		return ToolResult{IsError: true, Content: "content cannot be empty"}, nil
	}

	scope := ScopeFromContext(ctx)

	// Fetch existing entry to preserve metadata fields.
	entries, err := t.deps.Store.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to look up memory: %v", err)}, nil
	}

	var existing *store.MemoryEntry
	for i := range entries {
		if entries[i].ID == input.ID {
			existing = &entries[i]
			break
		}
	}

	if existing == nil {
		return ToolResult{Content: fmt.Sprintf("Memory not found: %s", input.ID)}, nil
	}

	updated := *existing
	updated.Content = input.Content
	updated.Title = truncateTitle(input.Content, 80)

	if err := t.deps.Store.UpdateMemory(ctx, scope, updated); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to update memory: %v", err)}, nil
	}

	if t.deps.EnqueueEnrich != nil {
		t.deps.EnqueueEnrich(updated)
	}
	if t.deps.EnqueueEmbed != nil {
		t.deps.EnqueueEmbed(updated.ID, scope, updated.Content)
	}

	return ToolResult{Content: fmt.Sprintf("Memory updated: %s", input.ID)}, nil
}

// ---------------------------------------------------------------------------
// forget_memory tool
// ---------------------------------------------------------------------------

type forgetMemoryTool struct {
	deps MemoryToolDeps
}

func (t *forgetMemoryTool) Name() string { return "forget_memory" }

func (t *forgetMemoryTool) Description() string {
	return "Forget/archive a memory that is no longer relevant. Use the memory ID from search_memory results."
}

func (t *forgetMemoryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string",
      "description": "The memory ID to forget (from search_memory results)"
    }
  }
}`)
}

type forgetMemoryParams struct {
	ID string `json:"id"`
}

func (t *forgetMemoryTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input forgetMemoryParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.ID) == "" {
		return ToolResult{IsError: true, Content: "id cannot be empty"}, nil
	}

	scope := ScopeFromContext(ctx)

	// Fetch existing entry to archive it.
	entries, err := t.deps.Store.SearchMemory(ctx, scope, "", 0)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to look up memory: %v", err)}, nil
	}

	var existing *store.MemoryEntry
	for i := range entries {
		if entries[i].ID == input.ID {
			existing = &entries[i]
			break
		}
	}

	if existing == nil {
		return ToolResult{Content: fmt.Sprintf("Memory not found: %s", input.ID)}, nil
	}

	now := time.Now().UTC()
	archived := *existing
	archived.ArchivedAt = &now

	if err := t.deps.Store.UpdateMemory(ctx, scope, archived); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("failed to forget memory: %v", err)}, nil
	}

	return ToolResult{Content: fmt.Sprintf("Memory forgotten: %s", input.ID)}, nil
}
