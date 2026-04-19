package agent

import (
	"log/slog"

	"daimon/internal/skill"
)

// InitSkillInjection separates skills into autoload (full prose) and indexed (on-demand).
// Budget is 20% of maxContextTokens. If exceeded, all skills fall to index-only.
// If maxContextTokens == 0, budget check is disabled.
func InitSkillInjection(allSkills []skill.SkillContent, maxContextTokens int) ([]skill.SkillContent, skill.SkillIndex) {
	var autoloaded []skill.SkillContent
	for _, s := range allSkills {
		if s.Autoload {
			autoloaded = append(autoloaded, s)
		}
	}

	index := skill.BuildIndex(allSkills) // non-autoload only

	// Skip budget check if disabled
	if maxContextTokens == 0 {
		return autoloaded, index
	}

	// Estimate tokens
	autoloadTokens := 0
	for _, s := range autoloaded {
		autoloadTokens += skill.EstimateTokens(s.Prose)
	}
	indexTokens := index.TokenEstimate()
	instructionTokens := 50 // overhead for instruction block
	total := autoloadTokens + indexTokens + instructionTokens

	budget := maxContextTokens * 20 / 100

	if total > budget {
		slog.Warn("skills: autoload+index exceeds 20% budget, falling back to index-only",
			"autoload_tokens", autoloadTokens,
			"index_tokens", indexTokens,
			"budget", budget,
		)
		// Fallback: rebuild index with ALL skills (including former autoloads)
		allIndex := skill.BuildIndex(forceAllToIndex(allSkills))
		return nil, allIndex
	}

	return autoloaded, index
}

// forceAllToIndex returns a copy of skills with Autoload set to false.
func forceAllToIndex(skills []skill.SkillContent) []skill.SkillContent {
	result := make([]skill.SkillContent, len(skills))
	for i, s := range skills {
		s.Autoload = false
		result[i] = s
	}
	return result
}
