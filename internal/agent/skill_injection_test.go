package agent

import (
	"strings"
	"testing"

	"daimon/internal/skill"
)

func TestInitSkillInjection(t *testing.T) {
	smallSkill := skill.SkillContent{
		Name:     "small-skill",
		Prose:    "tiny",
		Autoload: true,
	}
	largeSkill := skill.SkillContent{
		Name:     "large-skill",
		Prose:    strings.Repeat("x", 5000), // ~1250 tokens
		Autoload: true,
	}
	nonAutoloadSkill := skill.SkillContent{
		Name:        "on-demand",
		Description: "An on-demand skill",
		Prose:       "some prose",
		Autoload:    false,
	}

	tests := []struct {
		name             string
		skills           []skill.SkillContent
		maxContextTokens int
		wantAutoloadLen  int
		wantIndexLen     int
		wantNilAutoload  bool
	}{
		{
			name:             "under_budget",
			skills:           []skill.SkillContent{smallSkill, nonAutoloadSkill},
			maxContextTokens: 10000,
			wantAutoloadLen:  1,
			wantIndexLen:     1,
		},
		{
			name:             "over_budget",
			skills:           []skill.SkillContent{largeSkill},
			maxContextTokens: 100,
			wantAutoloadLen:  0,
			wantIndexLen:     1,
			wantNilAutoload:  true,
		},
		{
			name:             "budget_disabled",
			skills:           []skill.SkillContent{largeSkill},
			maxContextTokens: 0,
			wantAutoloadLen:  1,
			wantIndexLen:     0,
		},
		{
			name: "mixed_skills_filter_correctly",
			skills: []skill.SkillContent{
				{Name: "al-1", Prose: "small", Autoload: true},
				{Name: "al-2", Prose: "small", Autoload: true},
				{Name: "od-1", Description: "desc", Prose: "prose", Autoload: false},
				{Name: "od-2", Description: "desc", Prose: "prose", Autoload: false},
			},
			maxContextTokens: 10000,
			wantAutoloadLen:  2,
			wantIndexLen:     2,
		},
		{
			name: "all_autoload",
			skills: []skill.SkillContent{
				{Name: "a1", Prose: "x", Autoload: true},
				{Name: "a2", Prose: "y", Autoload: true},
			},
			maxContextTokens: 10000,
			wantAutoloadLen:  2,
			wantIndexLen:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			autoloaded, index := InitSkillInjection(tt.skills, tt.maxContextTokens)

			if tt.wantNilAutoload {
				if autoloaded != nil {
					t.Errorf("expected nil autoloaded, got %d items", len(autoloaded))
				}
			} else {
				if len(autoloaded) != tt.wantAutoloadLen {
					t.Errorf("autoloaded len = %d, want %d", len(autoloaded), tt.wantAutoloadLen)
				}
			}

			if len(index.Entries) != tt.wantIndexLen {
				t.Errorf("index entries len = %d, want %d", len(index.Entries), tt.wantIndexLen)
			}
		})
	}
}

func TestInitSkillInjection_OverBudgetIndexContainsAll(t *testing.T) {
	// When over budget, ALL skills (including former autoloads) should be in the index.
	skills := []skill.SkillContent{
		{Name: "big-skill", Prose: strings.Repeat("x", 5000), Autoload: true},
		{Name: "small-skill", Prose: "tiny", Autoload: false},
	}

	_, index := InitSkillInjection(skills, 100)

	// Both skills should be in the index (big-skill had Autoload=true but was forced down)
	if len(index.Entries) != 2 {
		t.Errorf("expected 2 index entries when over budget, got %d", len(index.Entries))
	}

	// Verify both skill names are present
	names := make(map[string]bool)
	for _, e := range index.Entries {
		names[e.Name] = true
	}
	if !names["big-skill"] {
		t.Error("expected 'big-skill' in index after budget fallback")
	}
	if !names["small-skill"] {
		t.Error("expected 'small-skill' in index after budget fallback")
	}
}

func TestForceAllToIndex(t *testing.T) {
	skills := []skill.SkillContent{
		{Name: "a", Autoload: true},
		{Name: "b", Autoload: false},
		{Name: "c", Autoload: true},
	}

	result := forceAllToIndex(skills)

	// Original skills should be unchanged
	for _, s := range skills {
		if s.Name == "a" || s.Name == "c" {
			if !s.Autoload {
				t.Error("original skill should still have Autoload=true")
			}
		}
	}

	// Result should have all Autoload=false
	for _, s := range result {
		if s.Autoload {
			t.Errorf("result skill %q should have Autoload=false", s.Name)
		}
	}

	if len(result) != len(skills) {
		t.Errorf("result len = %d, want %d", len(result), len(skills))
	}
}
