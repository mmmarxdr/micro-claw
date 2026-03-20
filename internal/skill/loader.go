package skill

import (
	"fmt"
	"os"
	"time"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// LoadSkills loads skill files from the given paths.
// Returns loaded prose contents, a map of tool.Tool implementations keyed by
// tool name, and a slice of non-fatal warnings (missing file, bad block, size limit).
// LoadSkills NEVER returns a fatal error; all failures are represented as warnings.
// The caller is responsible for logging returned warnings.
func LoadSkills(
	paths []string,
	shellCfg config.ShellToolConfig,
	limits config.LimitsConfig,
) ([]SkillContent, map[string]tool.Tool, []error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}

	tools := make(map[string]tool.Tool)
	var contents []SkillContent
	var warns []error
	var totalProseBytes int
	var warnedProseBudget bool

	for _, path := range paths {
		// Read file
		data, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, fmt.Errorf("skills: cannot read %q: %w", path, err))
			continue
		}

		// Size check (per-file limit: 8 KB)
		if len(data) > 8*1024 {
			warns = append(warns, fmt.Errorf("skills: file %q is too large (%d bytes, limit 8192); skipping", path, len(data)))
			continue
		}

		// Parse — use internal helper to avoid double I/O
		content, toolDefs, parseErrs := parseSkillContent(path, string(data))
		warns = append(warns, parseErrs...)

		// Track total prose bytes
		totalProseBytes += len(content.Prose)
		if totalProseBytes > 32*1024 && !warnedProseBudget {
			warns = append(warns, fmt.Errorf("skills: total skill prose exceeds 32 KB (%d bytes); system prompt will be large", totalProseBytes))
			warnedProseBudget = true
		}

		// Collect prose content
		contents = append(contents, content)

		// Build tool.Tool for each ToolDef, with env expansion
		for _, def := range toolDefs {
			// Expand env values
			expandedEnv := make(map[string]string, len(def.Env))
			for k, v := range def.Env {
				expanded, err := config.ExpandSafeEnv(v)
				if err != nil {
					warns = append(warns, fmt.Errorf("skills: tool %q env[%q]: %w; using unexpanded value", def.Name, k, err))
					expandedEnv[k] = v // use original on error
				} else {
					expandedEnv[k] = expanded
				}
			}
			def.Env = expandedEnv

			// Apply config inheritance for WorkingDir
			if def.WorkingDir == "" {
				def.WorkingDir = shellCfg.WorkingDir
			}

			// Apply config inheritance for Timeout
			if def.Timeout == 0 {
				def.Timeout = limits.ToolTimeout
			}
			if def.Timeout == 0 {
				def.Timeout = 30 * time.Second // absolute fallback
			}

			// Collision check: first skill file wins among skill files
			if _, exists := tools[def.Name]; exists {
				warns = append(warns, fmt.Errorf("skills: tool name %q defined in multiple skill files; first definition wins", def.Name))
				continue
			}

			tools[def.Name] = NewSkillShellTool(def)
		}
	}

	return contents, tools, warns
}
