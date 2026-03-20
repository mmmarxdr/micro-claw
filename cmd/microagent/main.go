package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"microagent/internal/agent"
	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	cronpkg "microagent/internal/cron"
	"microagent/internal/mcp"
	"microagent/internal/provider"
	"microagent/internal/setup"
	"microagent/internal/skill"
	"microagent/internal/store"
	"microagent/internal/tool"
	"microagent/internal/tui"
)

var (
	cfgPath     = flag.String("config", "", "path to config file (searches ~/.microagent/config.yaml and ./config.yaml if empty)")
	showVersion = flag.Bool("version", false, "print version and exit")
	dashboard   = flag.Bool("dashboard", false, "open read-only TUI dashboard and exit")
	runSetup    = flag.Bool("setup", false, "run the interactive setup wizard and exit")
	daemon      = flag.Bool("daemon", false, "run as background daemon (no interactive channel; cron only)")
)

// extractFlagValue scans args for "--flag value" or "--flag=value" and returns
// the value, or "" if not found. Used for pre-parse config path extraction.
func extractFlagValue(args []string, names ...string) string {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, n+"=") {
				return strings.TrimPrefix(a, n+"=")
			}
		}
	}
	return ""
}

func main() {
	// Subcommand dispatch — must precede flag.Parse() so that
	// "microagent mcp --help" does not trigger flag's unknown-flag error.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runMCPCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "skills" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runSkillsCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "cron" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runCronCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	flag.Parse()

	if *showVersion {
		fmt.Println("microagent dev")
		os.Exit(0)
	}

	if *runSetup {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(os.Stderr, "Setup wizard requires an interactive terminal.")
			os.Exit(1)
		}
		if _, err := setup.RunWizard(); err != nil {
			fmt.Fprintf(os.Stderr, "Setup wizard failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Dashboard path: requires a valid config and a TTY stdout.
	if *dashboard {
		if !isTTY(os.Stdout) {
			fmt.Fprintln(os.Stderr, "Dashboard requires an interactive terminal.")
			os.Exit(1)
		}
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			if errors.Is(err, config.ErrNoConfig) {
				fmt.Fprintln(os.Stderr, "No config file found. Cannot open dashboard.")
			} else {
				fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
			}
			os.Exit(1)
		}
		if err := tui.RunDashboard(cfg, *cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Normal / wizard path.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if errors.Is(err, config.ErrNoConfig) {
			if !isTTY(os.Stdin) {
				fmt.Fprintln(os.Stdout, "No config file found. Create one at ~/.microagent/config.yaml before running in non-interactive mode.")
				os.Exit(1)
			}
			if _, wizErr := setup.RunWizard(); wizErr != nil {
				fmt.Fprintf(os.Stderr, "Setup wizard failed: %v\n", wizErr)
				os.Exit(1)
			}
			// Re-attempt load after wizard writes config.
			cfg, err = config.Load(*cfgPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to load config after wizard: %v\n", err)
				os.Exit(1)
			}
		} else {
			slog.Error("failed to load configuration", "error", err)
			os.Exit(1)
		}
	}

	configureLogging(cfg.Logging)
	slog.Info("MicroAgent starting up")
	slog.Info("config loaded", "agent_name", cfg.Agent.Name)

	// Hoist context so MCP server connections (subprocess lifecycle) are tied
	// to the agent's root context from the start.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	toolsRegistry := tool.BuildRegistry(cfg.Tools)

	// Load skill files — non-fatal, warn+skip per file.
	// Skills are merged before MCP so that user-authored skill tools win on collision.
	// Priority: built-in > skill > MCP.
	var skillContents []skill.SkillContent
	if len(cfg.Skills) > 0 {
		var skillTools map[string]tool.Tool
		var skillWarns []error
		skillContents, skillTools, skillWarns = skill.LoadSkills(cfg.Skills, cfg.Tools.Shell, cfg.Limits)
		for _, w := range skillWarns {
			slog.Warn("skills: load warning", "error", w)
		}
		for name, t := range skillTools {
			if _, exists := toolsRegistry[name]; exists {
				slog.Warn("skills: tool collision with built-in, built-in wins", "tool", name)
				continue
			}
			toolsRegistry[name] = t
		}
	}

	// Connect to MCP servers and merge their tools into the registry.
	// Built-in and skill tools win on name collision.
	if cfg.Tools.MCP.Enabled {
		mcpTools, mcpManager, err := mcp.BuildMCPTools(ctx, cfg.Tools.MCP)
		if err != nil {
			slog.Error("mcp: failed to build MCP tools", "error", err)
			os.Exit(1)
		}
		defer mcpManager.Close()
		for name, t := range mcpTools {
			if _, exists := toolsRegistry[name]; exists {
				slog.Warn("mcp: tool name collision, existing tool wins", "tool", name)
				continue
			}
			toolsRegistry[name] = t
		}
	}

	prov, err := buildProvider(cfg.Provider)
	if err != nil {
		slog.Error("failed to create provider", "error", err)
		os.Exit(1)
	}
	slog.Info("provider initialized", "name", prov.Name(), "configured_model", cfg.Provider.Model)

	if cfg.Provider.Fallback != nil {
		fallbackProv, err := buildProvider(asProviderConfig(cfg.Provider.Fallback))
		if err != nil {
			slog.Error("failed to create fallback provider", "error", err)
			os.Exit(1)
		}
		// One-time startup warning when tool support is asymmetric.
		if prov.SupportsTools() && !fallbackProv.SupportsTools() {
			slog.Warn("fallback provider does not support tools; tool-calling disabled for this session",
				"primary", prov.Name(),
				"fallback", fallbackProv.Name(),
			)
		}
		prov = provider.NewFallbackProvider(prov, fallbackProv, slog.Default())
	}

	// Fail fast: verify API key and model connectivity early without consuming tokens
	hcCtx, hcCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hcCancel()
	verifiedName, err := prov.HealthCheck(hcCtx)
	if err != nil {
		slog.Error("provider health check failed, shutting down", "error", err)
		os.Exit(1)
	}
	slog.Info("provider health check passed", "verified_model_name", verifiedName)

	st, err := store.New(cfg.Store)
	if err != nil {
		slog.Error("failed to initialize store", "type", cfg.Store.Type, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Type-assert CronStore when cron is enabled.
	var cronSt store.CronStore
	if cfg.Cron.Enabled {
		cs, ok := st.(store.CronStore)
		if !ok {
			slog.Error("cron: store does not implement CronStore; set store.type = sqlite")
			os.Exit(1)
		}
		cronSt = cs
	}

	var auditor audit.Auditor = audit.NoopAuditor{}
	if cfg.Audit.Enabled {
		switch cfg.Audit.Type {
		case "sqlite":
			sa, err := audit.NewSQLiteAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize sqlite auditor, using noop", "error", err)
			} else {
				auditor = sa
				slog.Info("audit enabled", "type", "sqlite", "path", cfg.Audit.Path)
			}
		default: // "file" or anything unrecognised
			fa, err := audit.NewFileAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize file auditor, using noop", "error", err)
			} else {
				auditor = fa
				slog.Info("audit enabled", "type", "file", "path", cfg.Audit.Path)
			}
		}
	}

	// Build channels slice.
	var channels []channel.Channel

	// Build user-facing channel unless running in daemon mode.
	if !*daemon {
		switch cfg.Channel.Type {
		case "telegram":
			t, err := channel.NewTelegramChannel(cfg.Channel)
			if err != nil {
				slog.Error("failed to initialize telegram channel", "error", err)
				os.Exit(1)
			}
			channels = append(channels, t)
		case "cli", "":
			channels = append(channels, channel.NewCLIChannelDefault(cfg.Channel))
		default:
			slog.Error("unknown channel type", "type", cfg.Channel.Type)
			os.Exit(1)
		}
	}

	// Build cron subsystem if enabled.
	if cfg.Cron.Enabled {
		tz, _ := time.LoadLocation(cfg.Cron.Timezone)
		scheduler := cronpkg.NewScheduler(cronSt, tz, cfg.Cron.RetentionDays, cfg.Cron.MaxResultsPerJob)

		// origSender delivers cron results back to the user's real channel.
		// It's nil-safe: if channels is empty (daemon mode), Send is skipped by CronChannel.
		var origSender cronpkg.OriginalSender
		if len(channels) > 0 {
			userCh := channels[0]
			origSender = userCh.Send
		}

		cronChannel := cronpkg.NewCronChannel(scheduler, cronSt, origSender)
		channels = append(channels, cronChannel)

		// Merge cron tools into registry.
		cronTools := tool.BuildCronTools(scheduler, cronSt, toolsRegistry, prov)
		for name, t := range cronTools {
			if _, exists := toolsRegistry[name]; !exists {
				toolsRegistry[name] = t
			}
		}
	}

	if len(channels) == 0 {
		slog.Error("no channels configured; use --daemon with cron.enabled=true or configure a channel")
		os.Exit(1)
	}

	mux := channel.NewMultiplexChannel(channels)

	ag := agent.New(cfg.Agent, cfg.Limits, cfg.Filter, mux, prov, st, auditor, toolsRegistry, skillContents, cfg.Cron.MaxConcurrent)

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		slog.Info("shutting down")
		if err := ag.Shutdown(); err != nil {
			slog.Error("shutdown error", "error", err)
		}
		cancel()
	}()

	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("agent loop exited with error", "error", err)
	}

	slog.Info("MicroAgent exited")
}

// buildProvider constructs the appropriate Provider implementation from a ProviderConfig.
// Returns error defensively; config.Load() validates the type before main() runs.
func buildProvider(cfg config.ProviderConfig) (provider.Provider, error) {
	switch cfg.Type {
	case "gemini":
		return provider.NewGeminiProvider(cfg), nil
	case "openrouter":
		return provider.NewOpenRouterProvider(cfg), nil
	default:
		// Anthropic is the default (covers "" and "anthropic")
		return provider.NewAnthropicProvider(cfg), nil
	}
}

// asProviderConfig converts a FallbackConfig to ProviderConfig for buildProvider.
// MaxRetries is 0: the fallback activates only after the primary exhausted its retries;
// retrying again in the fallback would extend user-visible latency unnecessarily.
func asProviderConfig(fb *config.FallbackConfig) config.ProviderConfig {
	return config.ProviderConfig{
		Type:       fb.Type,
		Model:      fb.Model,
		APIKey:     fb.APIKey,
		BaseURL:    fb.BaseURL,
		Timeout:    fb.Timeout,
		MaxRetries: 0,
	}
}

// isTTY reports whether the given file is an interactive terminal.
// Handles both POSIX terminals and Cygwin/MinTTY on Windows.
func isTTY(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func configureLogging(cfg config.LoggingConfig) {
	// 1. Parse level (default: info)
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// 2. Choose output writer (default: stderr)
	var w io.Writer = os.Stderr
	if cfg.File != "" {
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			// Log warning to stderr before switching handler
			slog.Warn("failed to open log file, using stderr", "path", cfg.File, "error", err)
		} else {
			w = f
			// Intentionally not deferred: file is open for the lifetime of the process.
			// OS closes on exit. Using O_APPEND prevents corruption on fast restart.
		}
	}

	// 3. Build and install handler
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(cfg.Format) == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(h))
}
