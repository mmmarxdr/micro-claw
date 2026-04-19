package web

import (
	"context"
	"embed"
	"log/slog"
	"os"
	"path/filepath"

	"daimon/internal/config"
	"daimon/internal/skill"
)

//go:embed mcp_skills/*.md
var mcpSkillsFS embed.FS

// mcpRecipeSkills maps MCP server names to their bundled skill filenames.
var mcpRecipeSkills = map[string]string{
	"gmail":           "mcp-gmail.md",
	"google-workspace": "mcp-gmail.md",
	"google-calendar": "mcp-google-calendar.md",
	"github":          "mcp-github.md",
	"brave-search":    "mcp-brave-search.md",
}

// installRecipeSkill copies the bundled skill file for an MCP recipe to the
// user's skills directory and registers it in the config. It is a best-effort
// operation — failures are logged but do not prevent the MCP server from being added.
func installRecipeSkill(serverName string, cfg *config.Config, cfgPath string) {
	skillFile, ok := mcpRecipeSkills[serverName]
	if !ok {
		return
	}

	content, err := mcpSkillsFS.ReadFile("mcp_skills/" + skillFile)
	if err != nil {
		slog.Warn("mcp: bundled skill not found", "server", serverName, "file", skillFile, "error", err)
		return
	}

	// Resolve skills directory.
	skillsDir := cfg.SkillsDir
	if skillsDir == "" {
		home, _ := os.UserHomeDir()
		skillsDir = filepath.Join(home, ".microagent", "skills")
	} else {
		// Expand tilde.
		if len(skillsDir) > 0 && skillsDir[0] == '~' {
			home, _ := os.UserHomeDir()
			skillsDir = filepath.Join(home, skillsDir[1:])
		}
	}

	// Ensure directory exists.
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		slog.Warn("mcp: failed to create skills dir", "path", skillsDir, "error", err)
		return
	}

	// Write the skill file.
	destPath := filepath.Join(skillsDir, skillFile)
	if err := os.WriteFile(destPath, content, 0o644); err != nil {
		slog.Warn("mcp: failed to write skill file", "path", destPath, "error", err)
		return
	}

	// Register in config via SkillService (adds to skills[] list in YAML).
	svc := skill.NewSkillService(cfgPath, skillsDir, cfg.SkillsRegistryURL)
	if err := svc.Add(context.Background(), destPath, false); err != nil {
		// Already registered is fine.
		slog.Debug("mcp: skill registration note", "file", skillFile, "error", err)
	}

	slog.Info("mcp: installed recipe skill", "server", serverName, "skill", destPath)
}
