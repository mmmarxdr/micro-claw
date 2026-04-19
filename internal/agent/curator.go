package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"daimon/internal/config"
	"daimon/internal/content"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// classificationResult holds the structured output from the LLM classification call.
type classificationResult struct {
	Importance int    `json:"importance"`
	Type       string `json:"type"`
	Topic      string `json:"topic"`
	Title      string `json:"title"`
}

// validMemoryTypes is the set of recognised type values from classification.
var validMemoryTypes = map[string]bool{
	"fact":        true,
	"preference":  true,
	"instruction": true,
	"decision":    true,
	"context":     true,
	"skip":        true,
}

// Curator is a synchronous write-time intelligence layer that classifies,
// deduplicates, and selectively persists assistant responses as memory entries.
//
// Curator.Curate() is called synchronously in the agent loop after every
// non-empty LLM response. It is cheap (<1 s with a Haiku-class model) and
// does not need to be async.
//
// NewCurator returns nil when curation is disabled — callers must guard with
// a nil check before calling Curate.
type Curator struct {
	prov        provider.Provider
	store       store.Store
	enricher    *Enricher        // may be nil — async tag enrichment
	embWorker   *EmbeddingWorker // may be nil — async embedding
	curationCfg config.MemoryCurationConfig
	dedupCfg    config.DeduplicationConfig
	model       string         // cheap model resolved at construction time
	fillerRe    *regexp.Regexp // compiled filler pattern
}

// NewCurator constructs a Curator.
// Returns nil if curationCfg.Enabled is false.
func NewCurator(
	prov provider.Provider,
	st store.Store,
	enricher *Enricher,
	embWorker *EmbeddingWorker,
	curationCfg config.MemoryCurationConfig,
	dedupCfg config.DeduplicationConfig,
) *Curator {
	if !curationCfg.Enabled {
		return nil
	}

	minChars := curationCfg.MinResponseChars
	if minChars <= 0 {
		minChars = 50
	}
	curationCfg.MinResponseChars = minChars

	if curationCfg.MinImportance <= 0 {
		curationCfg.MinImportance = 5
	}

	if dedupCfg.MaxCandidates <= 0 {
		dedupCfg.MaxCandidates = 5
	}
	if dedupCfg.CosineThreshold <= 0 {
		dedupCfg.CosineThreshold = 0.85
	}

	// Filler pattern: short standalone tokens that carry no information value.
	fillerRe := regexp.MustCompile(`(?i)^(ok|sure|done|understood|got it|great|thanks|thank you|hello|hi|hey|yes|no|yep|nope|okay)[.!?]?\s*$`)

	return &Curator{
		prov:        prov,
		store:       st,
		enricher:    enricher,
		embWorker:   embWorker,
		curationCfg: curationCfg,
		dedupCfg:    dedupCfg,
		model:       resolveEnrichModel(prov, ""),
		fillerRe:    fillerRe,
	}
}

// shouldSkip performs cheap, LLM-free checks to decide whether a response is
// worth classifying at all. Returns true when the response should be dropped.
func (c *Curator) shouldSkip(response string) bool {
	trimmed := strings.TrimSpace(response)

	// Too short to be meaningful.
	if len(trimmed) < c.curationCfg.MinResponseChars {
		return true
	}

	// Refusal / "I don't know" patterns (case-insensitive prefix check).
	lower := strings.ToLower(trimmed)
	refusalPrefixes := []string{
		"i'm sorry", "i am sorry",
		"i cannot", "i can't",
		"i don't", "i do not",
		"lo siento", "no puedo",
	}
	for _, prefix := range refusalPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	// Pure filler — matches exact short tokens.
	if c.fillerRe.MatchString(trimmed) {
		return true
	}

	return false
}

// classify calls the LLM with a structured prompt to extract importance, type,
// topic, and a one-line title from the response text.
//
// On any error (network, timeout, parse failure), it returns a safe fallback
// so the caller can still save the entry at neutral importance.
func (c *Curator) classify(ctx context.Context, userMsg, response string) (classificationResult, error) {
	classCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	prompt := "Classify this assistant response for long-term memory storage.\n" +
		"Respond in JSON only: {\"importance\": 0-10, \"type\": \"fact|preference|instruction|decision|context|skip\", \"topic\": \"short-topic-label\", \"title\": \"one-line-summary\"}\n\n" +
		"Rules:\n" +
		"- importance 0-3: greetings, filler, errors, \"I don't know\" → type: skip\n" +
		"- importance 4-6: contextual info, partial answers, general knowledge\n" +
		"- importance 7-10: user preferences, personal facts, decisions, specific instructions\n\n" +
		"Response to classify:\n" + truncateForClassification(response, 500) + "\n\n" +
		"User message that prompted it:\n" + truncateForClassification(userMsg, 200)

	req := provider.ChatRequest{
		Model:        c.model,
		SystemPrompt: "You are a memory classification assistant. Respond ONLY with the requested JSON object.",
		Messages: []provider.ChatMessage{
			{Role: "user", Content: content.TextBlock(prompt)},
		},
		MaxTokens: 100,
	}

	resp, err := c.prov.Chat(classCtx, req)
	if err != nil {
		slog.Debug("curator: classify LLM call failed, using fallback", "error", err)
		return c.fallbackClassification(response), err
	}

	result, parseErr := parseClassificationJSON(resp.Content)
	if parseErr != nil {
		slog.Debug("curator: failed to parse classification JSON, using fallback",
			"raw", resp.Content, "error", parseErr)
		return c.fallbackClassification(response), nil
	}

	// Validate and clamp.
	if result.Importance < 0 {
		result.Importance = 0
	}
	if result.Importance > 10 {
		result.Importance = 10
	}
	if !validMemoryTypes[result.Type] {
		result.Type = "context"
	}
	if result.Title == "" {
		result.Title = truncateTitle(response, 80)
	}

	return result, nil
}

// fallbackClassification returns a neutral classification used when the LLM
// call fails or the response cannot be parsed.
func (c *Curator) fallbackClassification(response string) classificationResult {
	return classificationResult{
		Importance: 5,
		Type:       "context",
		Topic:      "",
		Title:      truncateTitle(response, 80),
	}
}

// parseClassificationJSON strips optional markdown code fences and unmarshals
// the JSON classification object.
func parseClassificationJSON(raw string) (classificationResult, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown code fence if present.
	if strings.HasPrefix(s, "```") {
		// Remove opening fence line.
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	// Some models wrap in { } at the start; try to find JSON object boundaries.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}

	var result classificationResult
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return classificationResult{}, err
	}
	return result, nil
}

// checkDedup searches existing memories for near-duplicates of content.
// Returns the ID of the duplicate entry if found, or "" if no duplicate exists.
//
// Strategy:
//  1. If embeddings are available on the store and a new embedding can be
//     generated: cosine similarity against stored embeddings.
//  2. Fallback: Jaccard similarity on word sets (> 0.7 threshold).
func (c *Curator) checkDedup(ctx context.Context, scope string, content string, result classificationResult) (string, error) {
	if !c.dedupCfg.Enabled {
		return "", nil
	}

	candidates, err := c.store.SearchMemory(ctx, scope, content, c.dedupCfg.MaxCandidates)
	if err != nil {
		return "", err
	}

	// Try to get an embedding for the new content if the store supports it.
	var newVec []float32
	if sqlSt, ok := c.store.(*store.SQLiteStore); ok && sqlSt.HasEmbedQueryFunc() {
		vec, embedErr := sqlSt.EmbedQuery(ctx, content)
		if embedErr == nil {
			newVec = vec
		}
	}

	for _, candidate := range candidates {
		// If both have embeddings, use cosine similarity.
		if newVec != nil && len(candidate.Embedding) > 0 {
			candidateVec := deserializeEmbedding(candidate.Embedding)
			sim := cosineSimilarity(newVec, candidateVec)
			if sim > c.dedupCfg.CosineThreshold {
				slog.Debug("curator: cosine duplicate found",
					"candidate_id", candidate.ID, "similarity", sim)
				return candidate.ID, nil
			}
		}

		// Fallback: Jaccard similarity on words.
		if jaccardSimilarity(content, candidate.Content) > 0.7 {
			slog.Debug("curator: Jaccard duplicate found", "candidate_id", candidate.ID)
			return candidate.ID, nil
		}
	}

	return "", nil
}

// Curate is the main entry point. It applies the full write-time intelligence
// pipeline: skip → classify → importance gate → dedup → save/update → enrich → embed.
//
// Returns nil on success. A non-nil error is returned only for unexpected
// store failures; skip decisions and low-importance gates return nil.
func (c *Curator) Curate(ctx context.Context, scope, userMsg, response, convID string) error {
	// 1. Fast-path skip — no LLM call needed.
	if c.shouldSkip(response) {
		slog.Debug("curator: skipping response (fast-path)", "scope", scope)
		return nil
	}

	// 2. Classify via LLM.
	result, classErr := c.classify(ctx, userMsg, response)
	if classErr != nil {
		// classify() already returned a fallback; use it but log the error.
		slog.Warn("curator: classification failed, using fallback", "error", classErr)
		// result is the fallback with importance=5; proceed with it.
	}

	slog.Debug("curator: classified response",
		"importance", result.Importance, "type", result.Type,
		"topic", result.Topic, "title", result.Title)

	// 3. Importance gate.
	if result.Importance < c.curationCfg.MinImportance {
		slog.Debug("curator: dropping low-importance response",
			"importance", result.Importance, "min", c.curationCfg.MinImportance)
		return nil
	}

	// 4. Skip type gate.
	if result.Type == "skip" {
		slog.Debug("curator: dropping skip-type response")
		return nil
	}

	// 5. Deduplication check.
	duplicateID, dedupErr := c.checkDedup(ctx, scope, response, result)
	if dedupErr != nil {
		slog.Warn("curator: dedup check failed, proceeding with new save", "error", dedupErr)
		// Fall through to new save.
	}

	if duplicateID != "" {
		// 6. Update existing duplicate entry.
		now := time.Now()
		updated := store.MemoryEntry{
			ID:         duplicateID,
			ScopeID:    scope,
			Content:    response,
			Topic:      result.Topic,
			Type:       result.Type,
			Title:      result.Title,
			Importance: result.Importance,
			Source:     convID,
			CreatedAt:  now,
		}
		if err := c.store.UpdateMemory(ctx, scope, updated); err != nil {
			return err
		}
		slog.Debug("curator: memory deduplicated", "existing_id", duplicateID)

		// Re-enqueue workers for the updated entry.
		if c.enricher != nil {
			c.enricher.Enqueue(updated)
		}
		if c.embWorker != nil {
			c.embWorker.Enqueue(updated.ID, scope, response)
		}
		return nil
	}

	// 7. Create new memory entry.
	entry := store.MemoryEntry{
		ID:         uuid.New().String(),
		ScopeID:    scope,
		Topic:      result.Topic,
		Type:       result.Type,
		Title:      result.Title,
		Content:    response,
		Source:     convID,
		Importance: result.Importance,
		CreatedAt:  time.Now(),
	}

	if err := c.store.AppendMemory(ctx, scope, entry); err != nil {
		return err
	}

	slog.Debug("curator: memory saved",
		"entry_id", entry.ID, "importance", entry.Importance, "topic", entry.Topic)

	// 8. Enqueue async workers.
	if c.enricher != nil {
		c.enricher.Enqueue(entry)
	}
	if c.embWorker != nil {
		c.embWorker.Enqueue(entry.ID, scope, response)
	}

	return nil
}

// ─── Helper functions ─────────────────────────────────────────────────────────

// truncateForClassification truncates s to at most maxChars, appending "..."
// when truncated.
func truncateForClassification(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "..."
}

// truncateTitle returns the first maxChars characters of s, stripped of
// newlines, suitable for a one-line memory title.
func truncateTitle(s string, maxChars int) string {
	// Replace newlines with spaces.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxChars {
		return s
	}
	return s[:maxChars] + "..."
}

// cosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0 if either vector is zero-length or they differ in length.
func cosineSimilarity(a, b []float32) float64 {
	n := len(a)
	if n == 0 || len(b) == 0 {
		return 0
	}
	if len(b) < n {
		n = len(b)
	}

	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// jaccardSimilarity computes the Jaccard index (intersection / union) of the
// word sets of strings a and b. Splitting is done on whitespace.
func jaccardSimilarity(a, b string) float64 {
	setA := wordSet(a)
	setB := wordSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1 // both empty → identical
	}

	var intersection int
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// wordSet splits s on whitespace and returns a set of lower-cased words.
func wordSet(s string) map[string]bool {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}
