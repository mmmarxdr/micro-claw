package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"daimon/internal/config"
	"daimon/internal/skill"
)

// runSkillsCommand dispatches to the appropriate skills subcommand handler.
// args is os.Args[2:] (everything after "skills").
// cfgPath is the resolved --config value (may be empty → config.FindConfigPath uses default search).
func runSkillsCommand(args []string, cfgPath string) error {
	if len(args) == 0 {
		fmt.Println("Usage: microagent skills <add|list|remove|info|search>")
		return nil
	}
	switch args[0] {
	case "add":
		return skillsAdd(args[1:], cfgPath)
	case "list":
		return skillsList(args[1:], cfgPath)
	case "remove", "rm":
		return skillsRemove(args[1:], cfgPath)
	case "info":
		return skillsInfo(args[1:], cfgPath)
	case "search":
		return skillsSearch(args[1:], cfgPath)
	case "--help", "-help", "-h":
		fmt.Println("Usage: microagent skills <add|list|remove|info|search>")
		return nil
	default:
		return fmt.Errorf("unknown skills subcommand: %q\nUsage: microagent skills <add|list|remove|info|search>", args[0])
	}
}

// skillsAdd implements `microagent skills add SRC [--force]`.
func skillsAdd(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("skills add", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite existing skill file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent skills add SRC [--force]")
		return fmt.Errorf("usage: microagent skills add SRC [--force]")
	}
	src := fs.Arg(0)

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return err
	}

	svc := skill.NewSkillService(resolved, cfg.SkillsDir, cfg.SkillsRegistryURL)
	ctx := context.Background()

	if err := svc.Add(ctx, src, *force); err != nil {
		if errors.Is(err, skill.ErrSkillExists) {
			fmt.Fprintf(os.Stderr, "Error: skill already exists (use --force to overwrite)\n")
		} else if errors.Is(err, skill.ErrNoRegistry) {
			fmt.Fprintf(os.Stderr, "Error: short name install requires a registry URL; set skills_registry_url in config\n")
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return err
	}

	fmt.Println("Skill installed successfully.")
	return nil
}

// skillsList implements `microagent skills list [--store]`.
func skillsList(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("skills list", flag.ContinueOnError)
	showStore := fs.Bool("store", false, "also scan the store dir for orphaned skill files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return err
	}

	svc := skill.NewSkillService(resolved, cfg.SkillsDir, cfg.SkillsRegistryURL)

	skills, err := svc.List(*showStore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing skills: %v\n", err)
		return err
	}

	if len(skills) == 0 {
		fmt.Println("No skills registered. Use 'microagent skills add' to install one.")
		return nil
	}

	// Build table with dynamic column widths.
	nameW, descW, pathW := 4, 11, 4 // minimum header widths
	for _, s := range skills {
		if len(s.Name) > nameW {
			nameW = len(s.Name)
		}
		if len(s.Description) > descW {
			descW = len(s.Description)
		}
		if len(s.Path) > pathW {
			pathW = len(s.Path)
		}
	}

	if *showStore {
		fmtRow := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, descW, pathW)
		fmt.Printf(fmtRow, "NAME", "DESCRIPTION", "PATH", "STATUS")
		for _, s := range skills {
			status := ""
			if s.Orphaned {
				status = "orphaned"
			}
			if s.ParseError != "" {
				status = "parse-error"
			}
			fmt.Printf(fmtRow, s.Name, s.Description, s.Path, status)
		}
	} else {
		fmtRow := fmt.Sprintf("%%-%ds  %%-%ds  %%s\n", nameW, descW)
		fmt.Printf(fmtRow, "NAME", "DESCRIPTION", "PATH")
		for _, s := range skills {
			fmt.Printf(fmtRow, s.Name, s.Description, s.Path)
		}
	}

	return nil
}

// skillsRemove implements `microagent skills remove NAME [--yes] [--keep-file]`.
func skillsRemove(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("skills remove", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	keepFile := fs.Bool("keep-file", false, "keep the skill file on disk, only unregister from config")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent skills remove NAME [--yes] [--keep-file]")
		return fmt.Errorf("usage: microagent skills remove NAME [--yes] [--keep-file]")
	}
	name := fs.Arg(0)

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(os.Stderr, "Error: --yes required when stdin is not a terminal")
			return fmt.Errorf("--yes required when stdin is not a terminal")
		}
		fmt.Printf("Remove skill %q? [y/N]: ", name)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		answer := strings.TrimSpace(scanner.Text())
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return err
	}

	svc := skill.NewSkillService(resolved, cfg.SkillsDir, cfg.SkillsRegistryURL)
	ctx := context.Background()

	if err := svc.Remove(ctx, name, !*keepFile); err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			fmt.Fprintf(os.Stderr, "Error: skill %q not found\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return err
	}

	fmt.Printf("Skill %q removed.\n", name)
	return nil
}

// skillsInfoFrontmatter holds extra frontmatter fields not in SkillContent.
type skillsInfoFrontmatter struct {
	Version string `yaml:"version"`
	Author  string `yaml:"author"`
}

// skillsInfo implements `microagent skills info NAME`.
func skillsInfo(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("skills info", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent skills info NAME")
		return fmt.Errorf("usage: microagent skills info NAME")
	}
	name := fs.Arg(0)

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return err
	}

	svc := skill.NewSkillService(resolved, cfg.SkillsDir, cfg.SkillsRegistryURL)

	content, tools, err := svc.Info(name)
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			fmt.Fprintf(os.Stderr, "Error: skill %q not found\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return err
	}

	// Derive the path for reading extra frontmatter fields (Version, Author).
	// Re-read the config to find the path.
	var fm skillsInfoFrontmatter
	var skillPath string
	{
		raw, readErr := os.ReadFile(resolved)
		if readErr == nil {
			var rawCfg struct {
				Skills []string `yaml:"skills"`
			}
			if unmarshalErr := yaml.Unmarshal(raw, &rawCfg); unmarshalErr == nil {
				for _, p := range rawCfg.Skills {
					derivedName := skillDeriveDisplayName(p)
					if derivedName == name {
						skillPath = p
						break
					}
				}
			}
		}
		if skillPath != "" {
			if data, readErr := os.ReadFile(skillPath); readErr == nil {
				lines := strings.Split(string(data), "\n")
				if len(lines) > 0 && strings.TrimRight(lines[0], "\r") == "---" {
					closingIdx := -1
					for j := 1; j < len(lines); j++ {
						if strings.TrimRight(lines[j], "\r") == "---" {
							closingIdx = j
							break
						}
					}
					if closingIdx > 0 {
						fmContent := strings.Join(lines[1:closingIdx], "\n")
						_ = yaml.Unmarshal([]byte(fmContent), &fm)
					}
				}
			}
		}
	}

	orNone := func(s string) string {
		if s == "" {
			return "(none)"
		}
		return s
	}

	fmt.Printf("Name:        %s\n", content.Name)
	fmt.Printf("Description: %s\n", orNone(content.Description))
	fmt.Printf("Version:     %s\n", orNone(fm.Version))
	fmt.Printf("Author:      %s\n", orNone(fm.Author))
	fmt.Printf("Path:        %s\n", orNone(skillPath))
	fmt.Println()
	fmt.Println("--- Content ---")
	if content.Prose == "" {
		fmt.Println("(none)")
	} else {
		fmt.Println(content.Prose)
	}
	fmt.Println()
	fmt.Println("--- Tools ---")
	for _, t := range tools {
		fmt.Printf("Tool: %s\n", t.Name)
		fmt.Printf("  Description: %s\n", t.Description)
		fmt.Printf("  Command:     %s\n", t.Command)
		if t.Timeout != 0 {
			fmt.Printf("  Timeout:     %s\n", t.Timeout)
		}
	}
	if len(tools) == 0 {
		fmt.Println("(none)")
	}

	return nil
}

// skillsSearch implements `microagent skills search [QUERY]`.
// It fetches the registry and prints skills matching the query.
// If no query is given, all available skills are listed.
func skillsSearch(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("skills search", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	query := fs.Arg(0) // optional; empty means list all

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return err
	}

	if cfg.SkillsRegistryURL == "" {
		fmt.Fprintln(os.Stderr, "Error: no registry URL configured (set skills_registry_url in config)")
		return fmt.Errorf("no registry URL configured")
	}

	ctx := context.Background()
	reg, err := skill.FetchRegistry(ctx, cfg.SkillsRegistryURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching registry: %v\n", err)
		return err
	}

	results := reg.Search(query)
	if len(results) == 0 {
		if query != "" {
			fmt.Printf("No skills found matching %q.\n", query)
		} else {
			fmt.Println("Registry is empty.")
		}
		return nil
	}

	// Determine column widths.
	nameW, descW, versionW := 4, 11, 7
	for _, e := range results {
		if len(e.Name) > nameW {
			nameW = len(e.Name)
		}
		if len(e.Description) > descW {
			descW = len(e.Description)
		}
		if len(e.Version) > versionW {
			versionW = len(e.Version)
		}
	}

	fmtRow := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, descW, versionW)
	fmt.Printf(fmtRow, "NAME", "DESCRIPTION", "VERSION", "TAGS")
	for _, e := range results {
		tags := strings.Join(e.Tags, ", ")
		fmt.Printf(fmtRow, e.Name, e.Description, e.Version, tags)
	}

	return nil
}

// skillDeriveDisplayName derives the display name for a skill path by reading the frontmatter name,
// falling back to the filename stem (matching the service's deriveSkillName logic).
func skillDeriveDisplayName(path string) string {
	content, _, errs := skill.ParseSkillFile(path)
	if len(errs) > 0 || content.Name == "" {
		// fallback to filename stem
		base := path
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[idx+1:]
		}
		return strings.TrimSuffix(base, ".md")
	}
	return content.Name
}
