package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"gopkg.in/yaml.v3"

	"microagent/internal/config"
)

// Sentinel errors for MCPService operations.
var (
	ErrDuplicateName = errors.New("mcp: server with that name already exists")
	ErrNotFound      = errors.New("mcp: server not found")
)

// validNameRe matches names containing only alphanumerics, hyphens, and underscores.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ServerStatus describes a configured server and its last-known state.
// Status fields (Connected, ToolCount, Error) are not persisted — they
// reflect the result of the most recent Test() call in this process only.
// At List() time, Connected is always false and ToolCount is always 0.
type ServerStatus struct {
	Config    config.MCPServerConfig
	Connected bool   // always false at list time — no live probe
	ToolCount int    // always 0 at list time
	Error     string // non-empty if last connection attempt failed (future use)
}

// MCPService is a stateless facade over the config file for MCP server management.
// It does NOT hold live connections — that remains Manager's responsibility.
// All mutating methods are safe for concurrent use: an in-process sync.Mutex
// guards the read-modify-write cycle, and a cross-process flock (Linux/macOS)
// prevents concurrent CLI invocations from racing on the same file.
type MCPService struct {
	cfgPath string
	mu      sync.Mutex
}

// NewMCPService returns a new MCPService that manages the config file at cfgPath.
// cfgPath must be the resolved absolute path (use config.FindConfigPath first).
func NewMCPService(cfgPath string) *MCPService {
	return &MCPService{cfgPath: cfgPath}
}

// List returns all configured MCP servers from the config file.
// No live connection is attempted; Connected is always false.
// Returns an empty (non-nil) slice when MCP is disabled or no servers are configured.
//
// IMPORTANT: This method reads raw YAML bytes directly (not via config.Load)
// to avoid expanding ${VAR} references — those are deferred to connect time.
func (s *MCPService) List(_ context.Context) ([]ServerStatus, error) {
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if !cfg.Tools.MCP.Enabled || len(cfg.Tools.MCP.Servers) == 0 {
		return []ServerStatus{}, nil
	}

	statuses := make([]ServerStatus, len(cfg.Tools.MCP.Servers))
	for i, srv := range cfg.Tools.MCP.Servers {
		statuses[i] = ServerStatus{Config: srv}
	}
	return statuses, nil
}

// Add appends a new MCPServerConfig to the YAML config file atomically.
// Returns ErrDuplicateName (wrapped) if a server with the same Name already exists.
// Returns a validation error if cfg fails Validate().
//
// Note: YAML comments in the config file are lost on round-trip due to yaml.v3
// marshal/unmarshal. Use 'microagent mcp validate' as a safety net after editing.
func (s *MCPService) Add(_ context.Context, cfg config.MCPServerConfig) error {
	if err := s.Validate(cfg); err != nil {
		return err
	}

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

	for _, srv := range fullCfg.Tools.MCP.Servers {
		if srv.Name == cfg.Name {
			return fmt.Errorf("server %q: %w", cfg.Name, ErrDuplicateName)
		}
	}

	fullCfg.Tools.MCP.Servers = append(fullCfg.Tools.MCP.Servers, cfg)
	return s.writeConfig(&fullCfg)
}

// Remove deletes a server entry from the YAML config file by name.
// Returns ErrNotFound (wrapped) if no server with the given name exists.
//
// Note: YAML comments in the config file are lost on round-trip due to yaml.v3
// marshal/unmarshal. Use 'microagent mcp validate' as a safety net after editing.
func (s *MCPService) Remove(_ context.Context, name string) error {
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

	original := fullCfg.Tools.MCP.Servers
	filtered := make([]config.MCPServerConfig, 0, len(original))
	found := false
	for _, srv := range original {
		if srv.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, srv)
	}

	if !found {
		return fmt.Errorf("server %q: %w", name, ErrNotFound)
	}

	fullCfg.Tools.MCP.Servers = filtered
	return s.writeConfig(&fullCfg)
}

// Validate checks an MCPServerConfig for structural correctness.
// It delegates to cfg.Validate() for transport-specific checks, and additionally
// enforces name rules. It does NOT check for duplicates — that is Add's responsibility.
// It does NOT perform any network or subprocess operation.
func (s *MCPService) Validate(cfg config.MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("mcp server name must not be empty")
	}
	if !validNameRe.MatchString(cfg.Name) {
		return fmt.Errorf("mcp server name %q: must match [a-zA-Z0-9_-]+", cfg.Name)
	}
	return cfg.Validate()
}

// Test performs a live connect → ListTools → Close cycle against the provided config.
// Callers SHOULD pass a context with a 15-second timeout for the CLI path.
// Returns the list of discovered tool names (with server prefix if PrefixTools is set).
// caller.Close() is always called before returning.
func (s *MCPService) Test(ctx context.Context, cfg config.MCPServerConfig) ([]string, error) {
	var connector ConnectorFunc
	switch cfg.Transport {
	case "stdio":
		connector = connectStdioListable
	case "http":
		connector = connectHTTPListable
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q", cfg.Transport)
	}
	return s.testWithConnector(ctx, cfg, connector)
}

// testWithConnector is the injectable core of Test, enabling unit tests to
// inject mock connectors without spawning real subprocesses or network connections.
func (s *MCPService) testWithConnector(ctx context.Context, cfg config.MCPServerConfig, connector ConnectorFunc) ([]string, error) {
	caller, err := connector(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = caller.Close() }()

	result, err := caller.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools from %q: %w", cfg.Name, err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		name := t.Name
		if cfg.PrefixTools {
			name = cfg.Name + "_" + t.Name
		}
		names = append(names, name)
	}
	return names, nil
}

// writeConfig marshals cfg to YAML and writes it atomically to s.cfgPath.
// Caller must hold s.mu before calling.
// The temp file is written at s.cfgPath+".tmp" (mode 0600) then renamed over the original.
func (s *MCPService) writeConfig(cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
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
