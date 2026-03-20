package skill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"microagent/internal/config"
)

// Sentinel errors for SkillService operations.
var (
	ErrSkillNotFound = errors.New("skill: not found")
	ErrSkillExists   = errors.New("skill: already exists in store (use --force to overwrite)")
	ErrNoRegistry    = errors.New("skill: no registry URL configured")
)

// InstalledSkill describes a skill entry as reported by List.
// Managed is true when the path is under the configured store dir.
// Orphaned is true when --store scan finds a file not in config.Skills.
type InstalledSkill struct {
	Name        string
	Description string
	Path        string
	Managed     bool   // true if path is under storeDir
	Orphaned    bool   // true if found in store but not in config.Skills (--store only)
	ParseError  string // non-empty if the file could not be parsed
}

// SkillService is a stateless facade over the config file and the on-disk skill store.
// All mutating methods are safe for concurrent use:
//   - an in-process sync.Mutex guards the read-modify-write cycle
//   - a cross-process flock prevents concurrent CLI invocations from racing on the same file
//
// Note: lockFile/unlockFile helpers are duplicated from internal/mcp/service_lock_*.go (ADR-4).
type SkillService struct {
	cfgPath     string
	storeDir    string
	registryURL string
	mu          sync.Mutex
}

// NewSkillService returns a new SkillService.
// cfgPath must be the resolved absolute path (use config.FindConfigPath first).
// storeDir must be the resolved absolute path to the managed skill store (config.SkillsDir after resolvePaths).
// registryURL is the base URL for the skill registry (config.SkillsRegistryURL); may be empty.
func NewSkillService(cfgPath, storeDir, registryURL string) *SkillService {
	return &SkillService{cfgPath: cfgPath, storeDir: storeDir, registryURL: registryURL}
}

// rawConfig is used for raw unmarshal of the config to preserve ${VAR} references.
type rawConfig struct {
	Skills []string `yaml:"skills"`
}

// List returns all skills registered in config.Skills.
// If showStore is true, it also scans storeDir for .md files and marks
// files present in the store but absent from config.Skills as Orphaned.
func (s *SkillService) List(showStore bool) ([]InstalledSkill, error) {
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg rawConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	var result []InstalledSkill

	// Track registered paths (normalized) for orphan detection.
	registeredPaths := make(map[string]bool, len(cfg.Skills))

	for _, p := range cfg.Skills {
		absPath := p
		if !filepath.IsAbs(absPath) {
			absPath, _ = filepath.Abs(absPath)
		}
		registeredPaths[absPath] = true

		entry := InstalledSkill{
			Path:    absPath,
			Managed: strings.HasPrefix(absPath, s.storeDir),
		}

		content, _, errs := ParseSkillFile(absPath)
		if len(errs) > 0 {
			// Use filename stem as name on parse error.
			entry.Name = filenameStem(absPath)
			entry.ParseError = errs[0].Error()
		} else {
			entry.Name = content.Name
			entry.Description = content.Description
		}

		result = append(result, entry)
	}

	if showStore {
		entries, err := os.ReadDir(s.storeDir)
		if err != nil {
			if os.IsNotExist(err) {
				// Store dir doesn't exist — return what we have, no error.
				return result, nil
			}
			return nil, fmt.Errorf("read store dir: %w", err)
		}

		for _, de := range entries {
			if de.IsDir() {
				continue
			}
			if filepath.Ext(de.Name()) != ".md" {
				continue
			}
			absPath := filepath.Join(s.storeDir, de.Name())
			if registeredPaths[absPath] {
				// Already in the registered list.
				continue
			}

			entry := InstalledSkill{
				Path:     absPath,
				Managed:  true,
				Orphaned: true,
			}

			content, _, errs := ParseSkillFile(absPath)
			if len(errs) > 0 {
				entry.Name = filenameStem(absPath)
				entry.ParseError = errs[0].Error()
			} else {
				entry.Name = content.Name
				entry.Description = content.Description
			}

			result = append(result, entry)
		}
	}

	if result == nil {
		result = []InstalledSkill{}
	}

	return result, nil
}

// Add installs a skill from src into the store and registers it in config.
// src may be an HTTP/HTTPS URL, a local file path (absolute, relative starting with . or /, or ~),
// or a short name resolved against the registry URL.
// If force is false and the destination file already exists, ErrSkillExists is returned.
func (s *SkillService) Add(ctx context.Context, src string, force bool) error {
	var rawBytes []byte
	var err error

	switch {
	case strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://"):
		// URL install — print security warning before fetching.
		fmt.Fprintf(os.Stderr,
			"WARNING: Installing a skill from a URL will write a file that executes shell commands\n"+
				"         with your user privileges. Only install skills from sources you trust.\n"+
				"         Source: %s\n", src)
		rawBytes, err = s.fetchURL(ctx, src)
		if err != nil {
			return fmt.Errorf("fetch skill: %w", err)
		}

	case strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~"):
		// Local file path.
		expanded := expandTilde(src)
		rawBytes, err = os.ReadFile(expanded)
		if err != nil {
			return fmt.Errorf("read skill file %q: %w", src, err)
		}

	default:
		// Short name — resolve via registry URL.
		if s.registryURL == "" {
			return ErrNoRegistry
		}
		registryURL := strings.TrimRight(s.registryURL, "/") + "/" + src + ".md"
		fmt.Fprintf(os.Stderr,
			"WARNING: Installing a skill from a URL will write a file that executes shell commands\n"+
				"         with your user privileges. Only install skills from sources you trust.\n"+
				"         Source: %s\n", registryURL)
		rawBytes, err = s.fetchURL(ctx, registryURL)
		if err != nil {
			return fmt.Errorf("fetch skill %q from registry: %w", src, err)
		}
	}

	// Validate the skill content by writing to a temp file and parsing it.
	tmpFile, err := os.CreateTemp("", "skill-validate-*.md")
	if err != nil {
		return fmt.Errorf("create temp file for validation: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(rawBytes); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file for validation: %w", err)
	}
	tmpFile.Close()

	skillContent, _, parseErrs := ParseSkillFile(tmpPath)
	if len(parseErrs) > 0 {
		return fmt.Errorf("skill parse error: %w", parseErrs[0])
	}

	// Derive skill name: from frontmatter Name if non-empty, else use src stem.
	name := skillContent.Name
	if name == "" {
		name = filenameStem(src)
	}
	// Sanitize: lowercase, spaces to hyphens.
	name = strings.ToLower(strings.ReplaceAll(name, " ", "-"))

	destPath := filepath.Join(s.storeDir, name+".md")

	if err := os.MkdirAll(s.storeDir, 0o700); err != nil {
		return fmt.Errorf("create store dir: %w", err)
	}

	if !force {
		if _, err := os.Stat(destPath); err == nil {
			return ErrSkillExists
		}
	}

	if err := os.WriteFile(destPath, rawBytes, 0o644); err != nil {
		return fmt.Errorf("write skill file: %w", err)
	}

	// Register in config, deduplicating existing entries.
	return s.writeConfig(func(cfg *config.Config) {
		// Deduplicate: remove any existing entry for this destPath.
		filtered := make([]string, 0, len(cfg.Skills)+1)
		for _, p := range cfg.Skills {
			if p != destPath {
				filtered = append(filtered, p)
			}
		}
		cfg.Skills = append(filtered, destPath)
	})
}

// Remove unregisters a skill by name from config.Skills.
// If deleteFile is true and the matched path is under storeDir, the file is also deleted.
func (s *SkillService) Remove(_ context.Context, name string, deleteFile bool) error {
	// Read config to find the matching path.
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg rawConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Find matching path by derived skill name.
	matchedPath := ""
	for _, p := range cfg.Skills {
		skillName := deriveSkillName(p)
		if skillName == name {
			matchedPath = p
			break
		}
	}

	if matchedPath == "" {
		return ErrSkillNotFound
	}

	// Remove from config.
	if err := s.writeConfig(func(cfg *config.Config) {
		filtered := make([]string, 0, len(cfg.Skills))
		for _, p := range cfg.Skills {
			if p != matchedPath {
				filtered = append(filtered, p)
			}
		}
		cfg.Skills = filtered
	}); err != nil {
		return err
	}

	// Delete file if requested and it's a managed path.
	if deleteFile && strings.HasPrefix(matchedPath, s.storeDir) {
		if err := os.Remove(matchedPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove skill file: %w", err)
		}
	}

	return nil
}

// Info parses the skill file for the named skill and returns its content and tools.
func (s *SkillService) Info(name string) (SkillContent, []ToolDef, error) {
	// Read config to find the matching path.
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return SkillContent{}, nil, fmt.Errorf("read config: %w", err)
	}

	var cfg rawConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return SkillContent{}, nil, fmt.Errorf("parse config: %w", err)
	}

	// Find matching path by derived skill name.
	matchedPath := ""
	for _, p := range cfg.Skills {
		skillName := deriveSkillName(p)
		if skillName == name {
			matchedPath = p
			break
		}
	}

	if matchedPath == "" {
		return SkillContent{}, nil, ErrSkillNotFound
	}

	content, tools, parseErrs := ParseSkillFile(matchedPath)
	if len(parseErrs) > 0 {
		return SkillContent{}, nil, fmt.Errorf("parse skill %q: %w", matchedPath, parseErrs[0])
	}

	return content, tools, nil
}

// writeConfig acquires s.mu, opens cfgPath, locks it, reads + unmarshals into config.Config,
// calls mutate to modify the config, marshals, writes to cfgPath+".tmp" (mode 0600), renames.
// Caller must NOT hold s.mu before calling.
func (s *SkillService) writeConfig(mutate func(*config.Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.cfgPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer func() { _ = unlockFile(f) }()

	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var fullCfg config.Config
	if err := yaml.Unmarshal(raw, &fullCfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	mutate(&fullCfg)

	data, err := yaml.Marshal(&fullCfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp := s.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}

	if err := os.Rename(tmp, s.cfgPath); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename config: %w", err)
	}

	return nil
}

// fetchURL fetches a URL with a 30s timeout and a 1 MB response size limit.
func (s *SkillService) fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}

	const maxSize = 1 << 20 // 1 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("fetch %s: response exceeds 1 MB limit", rawURL)
	}

	return data, nil
}

// deriveSkillName returns the skill name for a registered path.
// It parses the file to get the frontmatter Name; on error, falls back to the filename stem.
func deriveSkillName(path string) string {
	content, _, errs := ParseSkillFile(path)
	if len(errs) > 0 || content.Name == "" {
		return filenameStem(path)
	}
	return content.Name
}

// expandTilde expands a leading ~ to the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}
