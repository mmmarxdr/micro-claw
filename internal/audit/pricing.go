package audit

// modelPricing maps model IDs to per-1M-token pricing in USD.
var modelPricing = map[string]struct{ InputPer1M, OutputPer1M float64 }{
	// Anthropic
	"claude-sonnet-4-20250514":   {3.0, 15.0},
	"claude-haiku-3-5-20241022":  {0.80, 4.0},
	"claude-opus-4-20250514":     {15.0, 75.0},
	"claude-opus-4-5":            {15.0, 75.0},
	"claude-sonnet-4-5":          {3.0, 15.0},
	"claude-haiku-3-5":           {0.80, 4.0},
	"claude-3-5-sonnet-20241022": {3.0, 15.0},
	"claude-3-5-haiku-20241022":  {0.80, 4.0},
	"claude-3-opus-20240229":     {15.0, 75.0},

	// OpenAI
	"gpt-4o":        {2.50, 10.0},
	"gpt-4o-mini":   {0.15, 0.60},
	"gpt-4-turbo":   {10.0, 30.0},
	"gpt-4":         {30.0, 60.0},
	"gpt-3.5-turbo": {0.50, 1.50},
	"o1":            {15.0, 60.0},
	"o1-mini":       {3.0, 12.0},
	"o3-mini":       {1.10, 4.40},

	// Google Gemini
	"gemini-2.0-flash":       {0.075, 0.30},
	"gemini-2.0-flash-lite":  {0.075, 0.30},
	"gemini-1.5-pro":         {1.25, 5.0},
	"gemini-1.5-flash":       {0.075, 0.30},
	"gemini-1.5-flash-8b":    {0.0375, 0.15},
	"gemini-2.5-pro-preview": {1.25, 10.0},

	// OpenRouter pass-through pricing (approximate)
	"meta-llama/llama-3.1-8b-instruct":  {0.055, 0.055},
	"meta-llama/llama-3.1-70b-instruct": {0.40, 0.40},
	"mistralai/mistral-7b-instruct":     {0.055, 0.055},
	"mistralai/mixtral-8x7b-instruct":   {0.24, 0.24},
}

// EstimateCost returns the estimated USD cost for the given model and token counts.
// Returns 0 for unknown models (treat as free rather than erroring).
func EstimateCost(model string, inputTokens, outputTokens int64) float64 {
	p, ok := modelPricing[model]
	if !ok {
		return 0
	}
	return float64(inputTokens)/1_000_000*p.InputPer1M + float64(outputTokens)/1_000_000*p.OutputPer1M
}
