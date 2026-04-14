package agent

import (
	"context"
	"fmt"
	"sync"

	"microagent/internal/config"
	"microagent/internal/notify"
	"microagent/internal/provider"
)

// TokenUsage holds a breakdown of token usage for a given turn.
type TokenUsage struct {
	SystemPrompt int
	Messages     int
	Total        int
	Max          int
	UsagePercent float64
}

// LegacyTruncateFn is the signature for the legacy HistoryLength-based truncation function.
// It is wired by the agent so that the "legacy" strategy can delegate to the original
// legacyTruncate implementation without creating a circular dependency.
type LegacyTruncateFn func(ctx context.Context, messages []provider.ChatMessage) []provider.ChatMessage

// ContextManager tracks context window usage and drives proactive compaction.
// It is safe for concurrent use; mutable state is protected by a mutex.
type ContextManager struct {
	mu               sync.Mutex
	cfg              config.ContextConfig
	prov             provider.Provider  // for summarization calls
	bus              notify.Bus         // optional, for compaction events
	resolvedMaxToks  int                // resolved context window size
	lastCompactTurn  int                // hysteresis: last turn that compacted
	postCompactUsage int                // token count right after last compaction
	currentTurn      int                // incremented on every Manage call
	legacyFn         LegacyTruncateFn   // optional: called when strategy == "legacy"
}

// NewContextManager creates a new ContextManager, applying config defaults and
// resolving the context window size.
//
// For strategies "none" and "legacy", model detection is skipped to avoid
// unnecessary provider API calls at construction time.
func NewContextManager(cfg config.ContextConfig, prov provider.Provider, bus notify.Bus) *ContextManager {
	cfg.ApplyContextDefaults()
	// Skip expensive model-list detection for non-smart strategies.
	var maxToks int
	if cfg.Strategy == "smart" || cfg.Strategy == "" {
		maxToks = resolveContextSize(prov, cfg)
	} else {
		maxToks = cfg.FallbackCtxSize
	}
	return &ContextManager{
		cfg:             cfg,
		prov:            prov,
		bus:             bus,
		resolvedMaxToks: maxToks,
	}
}

// resolveContextSize determines the effective context window.
// Priority:
//  1. User override: cfg.ResolveMaxTokens() > 0 → use directly.
//  2. Auto-detect: type-assert prov to ModelLister, list models, find matching ID.
//  3. Fallback: cfg.FallbackCtxSize.
func resolveContextSize(prov provider.Provider, cfg config.ContextConfig) int {
	// 1. User override.
	if n := cfg.ResolveMaxTokens(); n > 0 {
		return n
	}

	// 2. Auto-detect via ModelLister.
	if ml, ok := prov.(provider.ModelLister); ok {
		models, err := ml.ListModels(context.Background())
		if err == nil {
			modelID := prov.Model()
			for _, m := range models {
				if m.ID == modelID {
					if m.ContextLength > 0 {
						return m.ContextLength
					}
				}
			}
		}
	}

	// 3. Fallback.
	return cfg.FallbackCtxSize
}

// MaxTokens returns the resolved context window size in tokens.
func (cm *ContextManager) MaxTokens() int {
	return cm.resolvedMaxToks
}

// Usage calculates a token usage breakdown for the given system prompt and messages.
// systemPromptTokens is pre-computed (caller uses EstimateTokens on the system prompt).
func (cm *ContextManager) Usage(systemPromptTokens int, messages []provider.ChatMessage) TokenUsage {
	msgTokens := EstimateMessagesTokens(messages)
	total := systemPromptTokens + msgTokens
	max := cm.resolvedMaxToks

	var pct float64
	if max > 0 {
		pct = float64(total) / float64(max) * 100
	}

	return TokenUsage{
		SystemPrompt: systemPromptTokens,
		Messages:     msgTokens,
		Total:        total,
		Max:          max,
		UsagePercent: pct,
	}
}

// Manage is the main entry point for proactive context management.
// It is called once per agent turn with the current messages slice.
// Returns a (possibly compacted) messages slice.
//
// Routing by strategy:
//   - "none"   → return messages unchanged
//   - "legacy" → legacyManage (stub: return unchanged)
//   - "smart"  → smartManage (threshold + hysteresis)
func (cm *ContextManager) Manage(
	ctx context.Context,
	systemPrompt string,
	toolDefs []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.currentTurn++

	switch cm.cfg.Strategy {
	case "none":
		return messages
	case "legacy":
		return cm.legacyManage(ctx, systemPrompt, toolDefs, messages)
	default: // "smart" and anything else
		return cm.smartManage(ctx, systemPrompt, toolDefs, messages)
	}
}

// ForceCompact bypasses threshold and cooldown checks and calls compactPipeline
// directly. Updates hysteresis state and emits a compaction event.
func (cm *ContextManager) ForceCompact(
	ctx context.Context,
	systemPrompt string,
	toolDefs []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	tokensBefore := EstimateMessagesTokens(messages)
	turnsBefore := len(findTurnBoundaries(messages))

	result := cm.compactPipeline(ctx, systemPrompt, toolDefs, messages)

	cm.lastCompactTurn = cm.currentTurn
	u := cm.Usage(EstimateTokens(systemPrompt), result)
	cm.postCompactUsage = u.Total

	tokensAfter := EstimateMessagesTokens(result)
	turnsAfter := len(findTurnBoundaries(result))
	turnsSummarized := turnsBefore - turnsAfter
	if turnsSummarized < 0 {
		turnsSummarized = 0
	}

	cm.emitCompactionEvent(tokensBefore, tokensAfter, turnsSummarized)
	return result
}

// smartManage implements threshold + hysteresis compaction logic.
func (cm *ContextManager) smartManage(
	ctx context.Context,
	systemPrompt string,
	toolDefs []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	// Calculate total token usage.
	sysToks := EstimateTokens(systemPrompt)
	toolToks := estimateToolDefTokens(toolDefs)
	msgToks := EstimateMessagesTokens(messages)
	total := sysToks + toolToks + msgToks
	max := cm.resolvedMaxToks

	// Compute thresholds.
	threshold := int(float64(max) * cm.cfg.CompactThreshold)
	hardMax := max // 100% of window

	// Below threshold → no compaction needed.
	if total < threshold {
		return messages
	}

	// Within cooldown AND below hard max → skip.
	turnsSinceLast := cm.currentTurn - cm.lastCompactTurn
	withinCooldown := cm.lastCompactTurn > 0 && turnsSinceLast < cm.cfg.CooldownTurns
	aboveHardMax := total >= hardMax

	if withinCooldown && !aboveHardMax {
		return messages
	}

	// Compact.
	tokensBefore := EstimateMessagesTokens(messages)
	turnsBefore := len(findTurnBoundaries(messages))

	result := cm.compactPipeline(ctx, systemPrompt, toolDefs, messages)
	cm.lastCompactTurn = cm.currentTurn
	u := cm.Usage(sysToks, result)
	cm.postCompactUsage = u.Total

	tokensAfter := EstimateMessagesTokens(result)
	turnsAfter := len(findTurnBoundaries(result))
	turnsSummarized := turnsBefore - turnsAfter
	if turnsSummarized < 0 {
		turnsSummarized = 0
	}

	cm.emitCompactionEvent(tokensBefore, tokensAfter, turnsSummarized)

	return result
}

// legacyManage delegates to the legacy HistoryLength-based truncation when legacyFn is set.
// When no legacyFn is wired, returns messages unchanged (safe no-op).
func (cm *ContextManager) legacyManage(
	ctx context.Context,
	_ string,
	_ []provider.ToolDefinition,
	messages []provider.ChatMessage,
) []provider.ChatMessage {
	if cm.legacyFn != nil {
		return cm.legacyFn(ctx, messages)
	}
	return messages
}

// compactPipeline implements the three-pass context compaction strategy.
// See compact_pipeline.go for the full implementation.
// It is defined there as a method on *ContextManager so that it can access
// all fields (cfg, prov, resolvedMaxToks) without needing them passed in.

// emitCompactionEvent emits a notify.EventContextCompacted event if the bus is
// wired and notifications are not explicitly disabled.
func (cm *ContextManager) emitCompactionEvent(tokensBefore, tokensAfter, turnsSummarized int) {
	if cm.bus == nil {
		return
	}
	if cm.cfg.Notify != nil && !*cm.cfg.Notify {
		return
	}
	cm.bus.Emit(notify.Event{
		Type:   notify.EventContextCompacted,
		Origin: notify.OriginAgent,
		Text:   fmt.Sprintf("Context compacted: %d → %d tokens", tokensBefore, tokensAfter),
		Meta: map[string]string{
			"tokens_before":    fmt.Sprintf("%d", tokensBefore),
			"tokens_after":     fmt.Sprintf("%d", tokensAfter),
			"turns_summarized": fmt.Sprintf("%d", turnsSummarized),
		},
	})
}

// estimateToolDefTokens estimates tokens for a slice of tool definitions.
func estimateToolDefTokens(toolDefs []provider.ToolDefinition) int {
	total := 0
	for _, td := range toolDefs {
		total += EstimateTokens(td.Name) + EstimateTokens(td.Description) + EstimateTokens(string(td.InputSchema))
	}
	return total
}
