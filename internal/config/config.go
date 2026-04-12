package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrNoConfig is returned by FindConfigPath when no configuration file is found.
// Callers should use errors.Is(err, ErrNoConfig) to distinguish this condition
// from parse errors or permission errors.
var ErrNoConfig = errors.New("no config file found")

// WebConfig holds configuration for the optional HTTP dashboard server.
type WebConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Port      int    `yaml:"port"`
	Host      string `yaml:"host"`
	AuthToken string `yaml:"auth_token"` // Bearer token for API/WS auth. Auto-generated if empty.
}

type Config struct {
	Agent             AgentConfig    `yaml:"agent"`
	Provider          ProviderConfig `yaml:"provider"`
	Channel           ChannelConfig  `yaml:"channel"`
	Tools             ToolsConfig    `yaml:"tools"`
	Store             StoreConfig    `yaml:"store"`
	Logging           LoggingConfig  `yaml:"logging"`
	Limits            LimitsConfig   `yaml:"limits"`
	Audit             AuditConfig    `yaml:"audit"`
	Cron              CronConfig     `yaml:"cron"`
	Filter            FilterConfig   `yaml:"filter"`
	Media             MediaConfig    `yaml:"media"`
	Web               WebConfig      `yaml:"web"`
	Skills            []string       `yaml:"skills"`
	SkillsDir         string         `yaml:"skills_dir"`
	SkillsRegistryURL string         `yaml:"skills_registry_url"`
}

// FilterConfig controls post-execution tool output compression.
// YAML key: filter
type FilterConfig struct {
	Enabled            bool         `yaml:"enabled"`          // default: false (opt-in)
	TruncationChars    int          `yaml:"truncation_chars"` // default: 8000
	Levels             FilterLevels `yaml:"levels"`
	InjectionDetection *bool        `yaml:"injection_detection"` // default: true — detect prompt injection in tool results
}

// FilterLevels configures per-tool-type filter aggressiveness.
type FilterLevels struct {
	Shell    string `yaml:"shell"`     // "aggressive" (default) | "minimal" | "no"
	FileRead string `yaml:"file_read"` // "minimal" (default) | "aggressive" | "no"
	Generic  bool   `yaml:"generic"`   // true (default when enabled) — apply generic truncation to unmatched tools
}

// ContextMode controls the native context-mode behavior.
type ContextMode string

const (
	ContextModeOff          ContextMode = "off"
	ContextModeConservative ContextMode = "conservative"
	ContextModeAuto         ContextMode = "auto"
)

// ContextModeConfig configures context-mode behavior.
type ContextModeConfig struct {
	Mode             ContextMode   `yaml:"mode"`               // default: "off"
	ShellMaxOutput   int           `yaml:"shell_max_output"`   // bytes, default 4096 (auto), 8192 (conservative)
	SandboxTimeout   time.Duration `yaml:"sandbox_timeout"`    // default 30s
	AutoIndexOutputs *bool         `yaml:"auto_index_outputs"` // default true in auto mode, false otherwise
	SandboxKeepFirst int           `yaml:"sandbox_keep_first"` // default 20 lines
	SandboxKeepLast  int           `yaml:"sandbox_keep_last"`  // default 10 lines
}

// CronConfig holds configuration for the cron scheduling subsystem.
type CronConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Timezone           string `yaml:"timezone"`
	RetentionDays      int    `yaml:"retention_days"`
	MaxResultsPerJob   int    `yaml:"max_results_per_job"`
	MaxConcurrent      int    `yaml:"max_concurrent"`
	NotifyOnCompletion bool   `yaml:"notify_on_completion"`
}

// AgentConfig holds all agent-level configuration.
type AgentConfig struct {
	Name             string `yaml:"name"`
	Personality      string `yaml:"personality"`
	MaxIterations    int    `yaml:"max_iterations"`
	MaxTokensPerTurn int    `yaml:"max_tokens_per_turn"`
	HistoryLength    int    `yaml:"history_length"`
	MemoryResults    int    `yaml:"memory_results"`
	MaxContextTokens int    `yaml:"max_context_tokens"` // token budget for context; 0 = use HistoryLength only
	SummaryTokens    int    `yaml:"summary_tokens"`     // max tokens for LLM-generated summaries

	// Native memory — Layer 2: LLM tag enrichment.
	EnrichMemory     bool   `yaml:"enrich_memory"`          // default: false — enables async tag enrichment
	EnrichModel      string `yaml:"enrich_model"`           // optional override for auto-selected cheap model
	EnrichRatePerMin int    `yaml:"enrich_rate_per_minute"` // default: 10 — enrichment calls per minute cap

	// Native memory — pruning.
	PruneInterval      time.Duration `yaml:"prune_interval"`       // default: 24h — how often to prune
	PruneRetentionDays int           `yaml:"prune_retention_days"` // default: 30 — days before archived entries are hard-deleted
	PruneThreshold     float64       `yaml:"prune_threshold"`      // default: 0.1 — minimum decay score to keep a memory

	// Native context-mode — token optimization for tool outputs.
	ContextMode ContextModeConfig `yaml:"context_mode"`
}

// FallbackConfig configures an optional secondary provider for resilience.
// When present, the runtime wraps the primary in a FallbackProvider decorator.
type FallbackConfig struct {
	Type    string        `yaml:"type"               json:"type"`
	Model   string        `yaml:"model"              json:"model"`
	APIKey  string        `yaml:"api_key"            json:"api_key"`
	BaseURL string        `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	Timeout time.Duration `yaml:"timeout"            json:"timeout"`
	Stream  *bool         `yaml:"stream"             json:"stream"`
}

type ProviderConfig struct {
	Type       string          `yaml:"type"`
	Model      string          `yaml:"model"`
	APIKey     string          `yaml:"api_key"`
	BaseURL    string          `yaml:"base_url"`
	Timeout    time.Duration   `yaml:"timeout"`
	MaxRetries int             `yaml:"max_retries"`
	Stream     *bool           `yaml:"stream"`
	Fallback   *FallbackConfig `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

type ChannelConfig struct {
	Type         string  `yaml:"type"`
	Token        string  `yaml:"token"` // e.g. for telegram
	AllowedUsers []int64 `yaml:"allowed_users"`

	// WhatsApp Cloud API fields
	PhoneNumberID string   `yaml:"phone_number_id"`
	AccessToken   string   `yaml:"access_token"`
	VerifyToken   string   `yaml:"verify_token"`
	WebhookPort   int      `yaml:"webhook_port"` // default 8080
	WebhookPath   string   `yaml:"webhook_path"` // default /webhook
	AllowedPhones []string `yaml:"allowed_phones"`

	// Discord fields (reserved for Discord agent)
	AllowedGuilds   []string `yaml:"allowed_guilds"`
	AllowedChannels []string `yaml:"allowed_channels"`
}

type ToolsConfig struct {
	Shell ShellToolConfig `yaml:"shell"`
	File  FileToolConfig  `yaml:"file"`
	HTTP  HTTPToolConfig  `yaml:"http"`
	MCP   MCPConfig       `yaml:"mcp"`
}

// MCPConfig is the top-level config block for MCP client support.
// YAML key: tools.mcp
type MCPConfig struct {
	Enabled        bool              `yaml:"enabled"`
	ConnectTimeout time.Duration     `yaml:"connect_timeout"` // default: 10s
	Servers        []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig describes one MCP server connection.
type MCPServerConfig struct {
	Name        string            `yaml:"name"`
	Transport   string            `yaml:"transport"`     // "stdio" | "http"
	Command     []string          `yaml:"command"`       // stdio only: [executable, args...]
	URL         string            `yaml:"url"`           // http only
	PrefixTools bool              `yaml:"prefix_tools"`  // prefix tool names with server name
	Env         map[string]string `yaml:"env,omitempty"` // extra env vars injected into the subprocess (stdio) or passed to HTTP headers (future)
}

// Validate returns an error if the server config is invalid.
// Called from Config.validate() when MCP is enabled.
func (s *MCPServerConfig) Validate() error {
	switch s.Transport {
	case "stdio":
		if len(s.Command) == 0 {
			return fmt.Errorf("mcp server %q: transport 'stdio' requires non-empty command", s.Name)
		}
	case "http":
		if s.URL == "" {
			return fmt.Errorf("mcp server %q: transport 'http' requires non-empty url", s.Name)
		}
	default:
		return fmt.Errorf("mcp server %q: unknown transport %q (must be 'stdio' or 'http')", s.Name, s.Transport)
	}
	return nil
}

type ShellToolConfig struct {
	Enabled         bool     `yaml:"enabled"`
	AllowedCommands []string `yaml:"allowed_commands"`
	AllowAll        bool     `yaml:"allow_all"`
	WorkingDir      string   `yaml:"working_dir"`
}

type FileToolConfig struct {
	Enabled     bool   `yaml:"enabled"`
	BasePath    string `yaml:"base_path"`
	MaxFileSize string `yaml:"max_file_size"`
}

type HTTPToolConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxResponseSize string        `yaml:"max_response_size"`
	BlockedDomains  []string      `yaml:"blocked_domains"`
}

// StoreConfig holds persistence layer configuration.
type StoreConfig struct {
	Type          string `yaml:"type"`
	Path          string `yaml:"path"`
	EncryptionKey string `yaml:"encryption_key,omitempty"` // hex-encoded 32-byte key; also read from MICROAGENT_SECRET_KEY env var

	// Native memory — Layer 3: optional API embeddings.
	// Requires store.type = "sqlite". When false (default), the embedding column
	// is still created by the migration but remains NULL for all rows.
	Embeddings bool `yaml:"embeddings"` // default: false
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	File   string `yaml:"file"`
}

type LimitsConfig struct {
	ToolTimeout  time.Duration `yaml:"tool_timeout"`
	TotalTimeout time.Duration `yaml:"total_timeout"`
}

type AuditConfig struct {
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"` // "file" (default) | "sqlite"
	Path    string `yaml:"path"`
}

// MediaConfig controls multimodal attachment handling.
// YAML key: media
//
// Enabled uses *bool (pointer) so that an explicitly set `enabled: false`
// is distinguishable from the omitted/unset case (nil → default true).
// Use BoolVal(cfg.Media.Enabled) to read the effective value.
type MediaConfig struct {
	Enabled             *bool         `yaml:"enabled"`
	MaxAttachmentBytes  int64         `yaml:"max_attachment_bytes"`
	MaxMessageBytes     int64         `yaml:"max_message_bytes"`
	RetentionDays       int           `yaml:"retention_days"`
	CleanupInterval     time.Duration `yaml:"cleanup_interval"`
	AllowedMIMEPrefixes []string      `yaml:"allowed_mime_prefixes"`
}

func (c *Config) applyDefaults() {
	if c.Agent.Name == "" {
		c.Agent.Name = "micro-claw"
	}
	if c.Agent.MaxIterations == 0 {
		c.Agent.MaxIterations = 10
	}
	if c.Agent.HistoryLength == 0 {
		c.Agent.HistoryLength = 20
	}
	if c.Agent.MemoryResults == 0 {
		c.Agent.MemoryResults = 5
	}
	if c.Agent.MaxTokensPerTurn == 0 {
		c.Agent.MaxTokensPerTurn = 4096
	}
	if c.Agent.MaxContextTokens == 0 {
		c.Agent.MaxContextTokens = 100000
	}
	if c.Agent.SummaryTokens == 0 {
		c.Agent.SummaryTokens = 1000
	}
	if c.Provider.Stream == nil {
		t := true
		c.Provider.Stream = &t
	}
	if c.Provider.Timeout == 0 {
		c.Provider.Timeout = 60 * time.Second
	}
	if c.Provider.MaxRetries == 0 {
		c.Provider.MaxRetries = 3
	}
	if (c.Provider.Type == "openai" || c.Provider.Type == "ollama") && c.Provider.Model == "" {
		c.Provider.Model = "gpt-4o"
	}
	if c.Tools.File.MaxFileSize == "" {
		c.Tools.File.MaxFileSize = "1MB"
	}
	if c.Tools.HTTP.Timeout == 0 {
		c.Tools.HTTP.Timeout = 15 * time.Second
	}
	if c.Limits.ToolTimeout == 0 {
		c.Limits.ToolTimeout = 30 * time.Second
	}
	if c.Limits.TotalTimeout == 0 {
		c.Limits.TotalTimeout = 120 * time.Second
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Store.Type == "" {
		c.Store.Type = "file"
	}
	if c.Store.Path == "" {
		c.Store.Path = "~/.microagent/data"
	}
	if c.Audit.Type == "" {
		c.Audit.Type = "file"
	}
	if c.Audit.Path == "" {
		c.Audit.Path = "~/.microagent/audit"
	}
	if c.Tools.MCP.ConnectTimeout == 0 {
		c.Tools.MCP.ConnectTimeout = 10 * time.Second
	}
	if c.SkillsDir == "" {
		c.SkillsDir = "~/.microagent/skills"
	}
	if c.SkillsRegistryURL == "" {
		c.SkillsRegistryURL = "https://raw.githubusercontent.com/mmmarxdr/micro-claw/main/configs/skills/registry.yaml"
	}
	if c.Cron.Timezone == "" {
		c.Cron.Timezone = "UTC"
	}
	if c.Cron.RetentionDays == 0 {
		c.Cron.RetentionDays = 30
	}
	if c.Cron.MaxResultsPerJob == 0 {
		c.Cron.MaxResultsPerJob = 50
	}
	if c.Cron.MaxConcurrent == 0 {
		c.Cron.MaxConcurrent = 4
	}
	if c.Channel.WebhookPort == 0 {
		c.Channel.WebhookPort = 8080
	}
	if c.Channel.WebhookPath == "" {
		c.Channel.WebhookPath = "/webhook"
	}
	// Native memory defaults.
	if c.Agent.EnrichRatePerMin == 0 {
		c.Agent.EnrichRatePerMin = 10
	}
	if c.Agent.PruneInterval == 0 {
		c.Agent.PruneInterval = 24 * time.Hour
	}
	if c.Agent.PruneRetentionDays == 0 {
		c.Agent.PruneRetentionDays = 30
	}
	if c.Agent.PruneThreshold == 0 {
		c.Agent.PruneThreshold = 0.1
	}
	if c.Filter.TruncationChars == 0 {
		c.Filter.TruncationChars = 8000
	}
	if c.Filter.Levels.Shell == "" {
		c.Filter.Levels.Shell = "aggressive"
	}
	if c.Filter.Levels.FileRead == "" {
		c.Filter.Levels.FileRead = "minimal"
	}
	// Generic defaults to true only when filter is enabled, since zero-value false
	// is the correct semantic when filter is disabled.
	if c.Filter.Enabled && !c.Filter.Levels.Generic {
		c.Filter.Levels.Generic = true
	}
	// InjectionDetection defaults to true (opt-out, not opt-in).
	if c.Filter.InjectionDetection == nil {
		t := true
		c.Filter.InjectionDetection = &t
	}

	// Media defaults.
	// Enabled is *bool: nil means omitted → default true; non-nil means explicitly set.
	// Other fields use zero-value sentinel: 0 / nil slice means omitted → apply default.
	if c.Media.Enabled == nil {
		t := true
		c.Media.Enabled = &t
	}
	if c.Media.MaxAttachmentBytes == 0 {
		c.Media.MaxAttachmentBytes = 10485760
	}
	if c.Media.MaxMessageBytes == 0 {
		c.Media.MaxMessageBytes = 26214400
	}
	if c.Media.RetentionDays == 0 {
		c.Media.RetentionDays = 30
	}
	if c.Media.CleanupInterval == 0 {
		c.Media.CleanupInterval = 24 * time.Hour
	}
	if len(c.Media.AllowedMIMEPrefixes) == 0 {
		c.Media.AllowedMIMEPrefixes = []string{"image/", "audio/", "application/pdf", "text/"}
	}

	// Web dashboard defaults (Enabled defaults to false).
	if c.Web.Port == 0 {
		c.Web.Port = 8080
	}
	if c.Web.Host == "" {
		c.Web.Host = "127.0.0.1"
	}
	// Auth token: config field → env var fallback.
	if c.Web.AuthToken == "" {
		if envToken := os.Getenv("MICROAGENT_WEB_TOKEN"); envToken != "" {
			c.Web.AuthToken = envToken
		}
	}

	// Context-mode defaults.
	if c.Agent.ContextMode.Mode == "" {
		c.Agent.ContextMode.Mode = ContextModeOff
	}

	// Set mode-specific defaults
	switch c.Agent.ContextMode.Mode {
	case ContextModeAuto:
		if c.Agent.ContextMode.ShellMaxOutput == 0 {
			c.Agent.ContextMode.ShellMaxOutput = 4096
		}
		// AutoIndexOutputs defaults to true in auto mode
		if c.Agent.ContextMode.AutoIndexOutputs == nil {
			t := true
			c.Agent.ContextMode.AutoIndexOutputs = &t
		}
	case ContextModeConservative:
		if c.Agent.ContextMode.ShellMaxOutput == 0 {
			c.Agent.ContextMode.ShellMaxOutput = 8192
		}
		// AutoIndexOutputs defaults to false for conservative (zero-value)
	case ContextModeOff:
		// Off mode doesn't need specific defaults for ShellMaxOutput
		// Values remain at zero
	}

	// Common defaults for all modes
	if c.Agent.ContextMode.SandboxTimeout == 0 {
		c.Agent.ContextMode.SandboxTimeout = 30 * time.Second
	}
	if c.Agent.ContextMode.SandboxKeepFirst == 0 {
		c.Agent.ContextMode.SandboxKeepFirst = 20
	}
	if c.Agent.ContextMode.SandboxKeepLast == 0 {
		c.Agent.ContextMode.SandboxKeepLast = 10
	}
}

// BoolVal safely dereferences a *bool, returning false if nil.
func BoolVal(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

func expandTilde(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

func (c *Config) resolvePaths() {
	c.Store.Path = expandTilde(c.Store.Path)
	c.Tools.File.BasePath = expandTilde(c.Tools.File.BasePath)
	c.Tools.Shell.WorkingDir = expandTilde(c.Tools.Shell.WorkingDir)
	c.Audit.Path = expandTilde(c.Audit.Path)
	c.SkillsDir = expandTilde(c.SkillsDir)
	for i, p := range c.Skills {
		c.Skills[i] = expandTilde(p)
	}
}

func (c *Config) validate() error {
	// Allow empty api_key for Ollama (type "ollama") and for OpenAI-compatible
	// endpoints with a custom base_url (e.g. local Ollama via type "openai").
	openAIWithCustomBase := c.Provider.Type == "openai" && c.Provider.BaseURL != ""
	if c.Provider.APIKey == "" && c.Provider.Type != "ollama" && !openAIWithCustomBase {
		return fmt.Errorf("provider.api_key is required")
	}
	switch c.Provider.Type {
	case "anthropic", "gemini", "openrouter", "openai", "ollama", "test", "test_provider", "":
		// valid
	default:
		return fmt.Errorf("unknown provider.type: %s", c.Provider.Type)
	}

	if c.Provider.Fallback != nil {
		if c.Provider.Fallback.APIKey == "" {
			return fmt.Errorf("provider.fallback.api_key is required")
		}
		if c.Provider.Fallback.Model == "" {
			return fmt.Errorf("provider.fallback.model is required")
		}
		switch c.Provider.Fallback.Type {
		case "anthropic", "gemini", "openrouter", "openai", "ollama", "test", "test_provider", "":
			// valid
		default:
			return fmt.Errorf("unknown provider.fallback.type: %s", c.Provider.Fallback.Type)
		}
	}

	switch c.Channel.Type {
	case "cli", "telegram", "discord", "whatsapp", "test_channel", "":
		// valid
	default:
		return fmt.Errorf("unknown channel.type: %s", c.Channel.Type)
	}

	if c.Channel.Type == "whatsapp" {
		if c.Channel.PhoneNumberID == "" {
			return fmt.Errorf("channel.phone_number_id is required for whatsapp channel")
		}
		if c.Channel.AccessToken == "" {
			return fmt.Errorf("channel.access_token is required for whatsapp channel")
		}
		if c.Channel.VerifyToken == "" {
			return fmt.Errorf("channel.verify_token is required for whatsapp channel")
		}
	}

	if c.Agent.MaxIterations <= 0 {
		return fmt.Errorf("agent.max_iterations must be positive")
	}
	if c.Limits.ToolTimeout > c.Limits.TotalTimeout {
		return fmt.Errorf("limits.tool_timeout cannot be greater than limits.total_timeout")
	}

	switch c.Store.Type {
	case "file", "sqlite", "":
		// valid
	default:
		return fmt.Errorf("unknown store.type: %s (must be 'file' or 'sqlite')", c.Store.Type)
	}

	if c.Tools.MCP.Enabled {
		for i := range c.Tools.MCP.Servers {
			if err := c.Tools.MCP.Servers[i].Validate(); err != nil {
				return err
			}
		}
	}

	if c.Cron.Enabled {
		if c.Store.Type != "sqlite" {
			return fmt.Errorf("cron requires store.type = 'sqlite', got %q", c.Store.Type)
		}
		if _, err := time.LoadLocation(c.Cron.Timezone); err != nil {
			return fmt.Errorf("cron.timezone %q is invalid: %w", c.Cron.Timezone, err)
		}
	}

	// Native memory validation.
	if c.Agent.EnrichMemory && c.Agent.EnrichRatePerMin <= 0 {
		return fmt.Errorf("agent.enrich_rate_per_minute must be positive when enrich_memory is true")
	}
	if c.Store.Embeddings && c.Store.Type != "sqlite" {
		return fmt.Errorf("store.embeddings requires store.type = 'sqlite'")
	}
	if c.Agent.PruneInterval <= 0 {
		return fmt.Errorf("agent.prune_interval must be positive")
	}
	if c.Agent.PruneRetentionDays <= 0 {
		return fmt.Errorf("agent.prune_retention_days must be positive")
	}
	if c.Agent.PruneThreshold < 0 || c.Agent.PruneThreshold > 1.0 {
		return fmt.Errorf("agent.prune_threshold must be between 0 and 1.0")
	}

	// Media validation — skipped entirely when disabled (kill switch).
	if BoolVal(c.Media.Enabled) {
		if c.Media.MaxAttachmentBytes < 1024 || c.Media.MaxAttachmentBytes > 52428800 {
			return fmt.Errorf("media.max_attachment_bytes must be between 1024 and 52428800, got %d", c.Media.MaxAttachmentBytes)
		}
		if c.Media.MaxMessageBytes < c.Media.MaxAttachmentBytes {
			return fmt.Errorf("media.max_message_bytes (%d) must be >= media.max_attachment_bytes (%d)", c.Media.MaxMessageBytes, c.Media.MaxAttachmentBytes)
		}
		if c.Media.RetentionDays < 1 {
			return fmt.Errorf("media.retention_days must be >= 1, got %d", c.Media.RetentionDays)
		}
		if c.Media.CleanupInterval < time.Hour {
			return fmt.Errorf("media.cleanup_interval must be >= 1h, got %s", c.Media.CleanupInterval)
		}
		if len(c.Media.AllowedMIMEPrefixes) == 0 {
			return fmt.Errorf("media.allowed_mime_prefixes must not be empty when media.enabled=true")
		}
	}

	// Web dashboard validation.
	if c.Web.Enabled {
		if c.Web.Port < 1024 || c.Web.Port > 65535 {
			return fmt.Errorf("web.port must be in [1024, 65535], got %d", c.Web.Port)
		}
		// T3.2: port conflict check with WhatsApp webhook port.
		if c.Channel.Type == "whatsapp" && c.Channel.WebhookPort == c.Web.Port {
			return fmt.Errorf("web.port (%d) conflicts with channel.webhook_port (%d)", c.Web.Port, c.Channel.WebhookPort)
		}
	}

	// Context-mode validation.
	if c.Agent.ContextMode.Mode != ContextModeOff &&
		c.Agent.ContextMode.Mode != ContextModeConservative &&
		c.Agent.ContextMode.Mode != ContextModeAuto {
		return fmt.Errorf("agent.context_mode.mode must be 'off', 'conservative', or 'auto', got %q", c.Agent.ContextMode.Mode)
	}
	if c.Agent.ContextMode.Mode != ContextModeOff {
		if c.Agent.ContextMode.ShellMaxOutput < 0 {
			return fmt.Errorf("agent.context_mode.shell_max_output must not be negative")
		}
		if c.Agent.ContextMode.SandboxTimeout <= 0 {
			return fmt.Errorf("agent.context_mode.sandbox_timeout must be positive")
		}
		if c.Agent.ContextMode.SandboxKeepFirst < 0 {
			return fmt.Errorf("agent.context_mode.sandbox_keep_first must not be negative")
		}
		if c.Agent.ContextMode.SandboxKeepLast < 0 {
			return fmt.Errorf("agent.context_mode.sandbox_keep_last must not be negative")
		}
	}

	return nil
}

// ExpandSafeEnv expands ${VAR} references in s using os.LookupEnv.
// It returns an error if any referenced variable is not set in the environment.
// Malformed references (e.g. ${PARTIAL) are left as-is.
func ExpandSafeEnv(s string) (string, error) {
	// os.ExpandEnv simply removes unresolvable chunks. We want to catch ones that are explicitly meant as variables but missing,
	// except if they are malformed like ${PARTIAL. A regex gives us more control.
	re := regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
	var validationErr error

	expanded := re.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		val, exists := os.LookupEnv(varName)
		if !exists {
			validationErr = fmt.Errorf("required environment variable %s is not set", varName)
			return match
		}
		return val
	})

	return expanded, validationErr
}

func FindConfigPath(override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("config file not found at %s", override)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		homePath := filepath.Join(home, ".microagent/config.yaml")
		if _, err := os.Stat(homePath); err == nil {
			return homePath, nil
		}
	}

	localPath := "./config.yaml"
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	return "", ErrNoConfig
}

func Load(path string) (*Config, error) {
	resolvedPath, err := FindConfigPath(path)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Treat a completely blank file the same as a missing config.
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("load: %w", ErrNoConfig)
	}

	expanded, err := ExpandSafeEnv(string(data))
	if err != nil {
		return nil, fmt.Errorf("expanding env vars: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	cfg.resolvePaths()

	return &cfg, nil
}
