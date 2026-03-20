package skill

import "time"

// SkillContent holds the parsed behavioral content from a skill file.
// Used by the agent to inject behavioural instructions into the system prompt.
type SkillContent struct {
	Name        string // from frontmatter; fallback: filename stem (no extension)
	Description string // from frontmatter; optional, informational only
	Prose       string // everything except frontmatter and ```yaml tool blocks
}

// ToolDef is the parsed representation of a ```yaml tool fenced block.
// All fields map 1:1 to YAML keys inside the fenced block.
type ToolDef struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Command     string            `yaml:"command"`
	Timeout     time.Duration     `yaml:"timeout"`     // 0 = inherit limits.tool_timeout
	WorkingDir  string            `yaml:"working_dir"` // "" = inherit tools.shell.working_dir
	Env         map[string]string `yaml:"env"`         // values expanded at load time
}
