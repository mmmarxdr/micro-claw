package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// validName matches acceptable tool names: lowercase letter, then lowercase letters, digits, or underscores.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// state represents the parser state machine states.
type state int

const (
	stBody        state = iota
	stFrontmatter state = iota
	stToolBlock   state = iota
)

// frontmatter holds the parsed YAML frontmatter fields.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Version     string `yaml:"version"`
	Author      string `yaml:"author"`
}

// openFence reports whether a line opens a yaml tool block.
// The info string must be exactly "yaml tool" — case-sensitive.
func openFence(line string) bool {
	return line == "```yaml tool"
}

// closeFence reports whether a line closes a fenced block.
func closeFence(line string) bool {
	return line == "```"
}

// filenameStem derives the skill name from a path when frontmatter has no name.
// e.g. "/home/user/.microagent/skills/git-helper.md" → "git-helper"
func filenameStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// validateToolDef checks that a ToolDef has all required fields and a valid name.
func validateToolDef(def ToolDef) error {
	if def.Name == "" {
		return fmt.Errorf("tool block missing required field: name")
	}
	if !validName.MatchString(def.Name) {
		return fmt.Errorf("tool name %q is invalid: must match ^[a-z][a-z0-9_]*$", def.Name)
	}
	if def.Description == "" {
		return fmt.Errorf("tool %q missing required field: description", def.Name)
	}
	if def.Command == "" {
		return fmt.Errorf("tool %q missing required field: command", def.Name)
	}
	return nil
}

// trimBlankLines removes leading and trailing blank lines from a string.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	// trim leading blank lines
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	// trim trailing blank lines
	end := len(lines)
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// ParseSkillFile parses a single skill markdown file from disk.
// Returns the prose content, list of tool definitions, and any parse-time warnings.
// Non-fatal errors (e.g. bad tool block) are returned alongside partial results.
func ParseSkillFile(path string) (SkillContent, []ToolDef, []error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillContent{}, nil, []error{fmt.Errorf("skill: cannot read %q: %w", path, err)}
	}
	return parseSkillContent(path, string(data))
}

// parseSkillContent parses the content of a skill file given the path (for name fallback) and content string.
func parseSkillContent(path string, content string) (SkillContent, []ToolDef, []error) {
	var errs []error
	var tools []ToolDef

	lines := strings.Split(content, "\n")
	// Handle Windows-style line endings
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}

	// Remove trailing empty line that results from splitting a file ending in \n
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var fm frontmatter
	hasFrontmatter := false
	var proseSegments []string
	var currentSegment []string
	var toolBlockLines []string
	st := stBody

	i := 0

	// Check for frontmatter: first line must be exactly "---"
	if len(lines) > 0 && lines[0] == "---" {
		// Look for closing "---"
		closingIdx := -1
		for j := 1; j < len(lines); j++ {
			if lines[j] == "---" {
				closingIdx = j
				break
			}
		}

		if closingIdx > 0 {
			// We have a valid frontmatter block
			fmContent := strings.Join(lines[1:closingIdx], "\n")
			if err := yaml.Unmarshal([]byte(fmContent), &fm); err != nil {
				// Malformed YAML: treat entire file as prose (no error emitted per spec FM-7)
				// Reset fm to zero value and treat all lines as body
				fm = frontmatter{}
				// Fall through to process all lines as body
			} else {
				hasFrontmatter = true
				i = closingIdx + 1
			}
		}
		// If no closing "---" found, treat entire file as prose (spec FM-4)
	}

	// Process body lines
	st = stBody
	for ; i < len(lines); i++ {
		line := lines[i]
		switch st {
		case stBody:
			if openFence(line) {
				// Save current prose segment
				proseSegments = append(proseSegments, strings.Join(currentSegment, "\n"))
				currentSegment = nil
				toolBlockLines = nil
				st = stToolBlock
			} else {
				currentSegment = append(currentSegment, line)
			}
		case stToolBlock:
			if closeFence(line) {
				// Try to unmarshal and validate the tool block
				blockYAML := strings.Join(toolBlockLines, "\n")
				var def ToolDef
				if err := yaml.Unmarshal([]byte(blockYAML), &def); err != nil {
					errs = append(errs, fmt.Errorf("skill %q: tool block YAML parse error: %w", path, err))
				} else if err := validateToolDef(def); err != nil {
					errs = append(errs, fmt.Errorf("skill %q: %w", path, err))
				} else {
					tools = append(tools, def)
				}
				toolBlockLines = nil
				st = stBody
			} else {
				toolBlockLines = append(toolBlockLines, line)
			}
		}
	}

	// Collect any remaining prose segment
	if len(currentSegment) > 0 {
		proseSegments = append(proseSegments, strings.Join(currentSegment, "\n"))
	}

	// Build prose: trim each segment's leading/trailing blank lines, join with "\n\n"
	var trimmedSegments []string
	for _, seg := range proseSegments {
		trimmed := trimBlankLines(seg)
		if trimmed != "" {
			trimmedSegments = append(trimmedSegments, trimmed)
		}
	}
	prose := strings.Join(trimmedSegments, "\n\n")

	// Determine name
	name := fm.Name
	if name == "" {
		name = filenameStem(path)
	}

	_ = hasFrontmatter // used implicitly via fm

	skillContent := SkillContent{
		Name:        name,
		Description: fm.Description,
		Prose:       prose,
	}

	return skillContent, tools, errs
}
