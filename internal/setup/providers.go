package setup

// ModelInfo describes one model in the embedded 2026 catalog.
// All fields are populated for every catalog entry.
type ModelInfo struct {
	ID          string  // API identifier used verbatim in config (e.g. "claude-sonnet-4-6")
	DisplayName string  // Human-readable label (e.g. "Claude Sonnet 4.6")
	CostIn      float64 // USD cost per 1M input tokens (0.0 for free-tier models)
	CostOut     float64 // USD cost per 1M output tokens (0.0 for free-tier models)
	ContextK    int     // Context window in thousands of tokens; 0 means unknown/variable
	Description string  // One-line English description shown in picker
}

// OtherModelSentinel is the sentinel entry appended to every provider list by
// newModelSelectorModel. When the user selects this entry, modelSelectorModel
// switches to free-text mode. It is NOT included in ProviderCatalog directly.
var OtherModelSentinel = ModelInfo{
	ID:          "",
	DisplayName: "Other...",
	Description: "Type any model ID not listed above",
}

// ProviderCatalog maps provider type strings to their curated model lists.
// Ollama is intentionally absent — it always uses free-text entry because
// available models depend on the user's local pull history.
// List is current as of early 2026; use Other... for newer models.
var ProviderCatalog = map[string][]ModelInfo{
	"anthropic": {
		{
			ID:          "claude-sonnet-4-6",
			DisplayName: "Claude Sonnet 4.6",
			CostIn:      3.00,
			CostOut:     15.00,
			ContextK:    400, // >400K; stored as 400 (display as ">400K")
			Description: "Balanced cost/perf, excels at code",
		},
		{
			ID:          "claude-opus-4-6",
			DisplayName: "Claude Opus 4.6",
			CostIn:      5.00,
			CostOut:     25.00,
			ContextK:    1000, // 1M tokens; stored as 1000 (display as "1M")
			Description: "Max intelligence, complex reasoning",
		},
	},
	"gemini": {
		{
			ID:          "gemini-3.1-flash-lite",
			DisplayName: "Gemini 3.1 Flash-Lite",
			CostIn:      0.25,
			CostOut:     1.50,
			ContextK:    0, // context not specified
			Description: "High speed, low cost, best for high volume",
		},
		{
			ID:          "gemini-3.1-pro",
			DisplayName: "Gemini 3.1 Pro",
			CostIn:      2.00,
			CostOut:     12.00,
			ContextK:    200,
			Description: "Advanced reasoning, complex code, agentic flows",
		},
	},
	"openai": {
		{
			ID:          "gpt-5.4",
			DisplayName: "GPT-5.4",
			CostIn:      2.50,
			CostOut:     15.00,
			ContextK:    1000,
			Description: "General purpose, desktop control, very efficient",
		},
		{
			ID:          "gpt-5.4-pro",
			DisplayName: "GPT-5.4 Pro",
			CostIn:      30.00,
			CostOut:     180.00,
			ContextK:    1000,
			Description: "Extreme scientific performance, high cost",
		},
	},
	"openrouter": {
		{
			ID:          "openrouter/free",
			DisplayName: "OpenRouter Free",
			CostIn:      0.00,
			CostOut:     0.00,
			ContextK:    200,
			Description: "Free routing, vision+tools (20 req/min limit)",
		},
		{
			ID:          "qwen/qwen3-coder:free",
			DisplayName: "Qwen 3 Coder",
			CostIn:      0.00,
			CostOut:     0.00,
			ContextK:    262,
			Description: "Best free coding model",
		},
		{
			ID:          "openrouter/healer-alpha",
			DisplayName: "Healer Alpha",
			CostIn:      0.00,
			CostOut:     0.00,
			ContextK:    262,
			Description: "Agentic omni-modal AI (data logged)",
		},
	},
	// "ollama" is intentionally absent — free-text only, no catalog
}

// ModelsForProvider returns the curated model list for a provider.
// Returns nil for unknown providers or "ollama" (free-text only).
// The returned slice does NOT include OtherModelSentinel; callers append it as needed.
func ModelsForProvider(provider string) []ModelInfo {
	return ProviderCatalog[provider]
}
