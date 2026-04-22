package main

// Binary size baseline: ~21M as of multimodal-messages change (content package + media_blobs + media helpers).

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

	"daimon/internal/agent"
	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	cronpkg "daimon/internal/cron"
	"daimon/internal/mcp"
	"daimon/internal/notify"
	"daimon/internal/provider"
	"daimon/internal/setup"
	"daimon/internal/skill"
	"daimon/internal/store"
	"daimon/internal/tool"
	"daimon/internal/tui"
	"daimon/internal/web"
)

var (
	// Build-time variables set via -ldflags by goreleaser. Defaults flag the
	// binary as a development build so `daimon update` refuses to self-replace
	// it with a release.
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var (
	cfgPath        = flag.String("config", "", "path to config file (searches ~/.daimon/config.yaml and ./config.yaml if empty)")
	showVersion    = flag.Bool("version", false, "print version and exit")
	dashboard      = flag.Bool("dashboard", false, "open read-only TUI dashboard and exit")
	runSetup       = flag.Bool("setup", false, "run the interactive setup wizard and exit")
	daemon         = flag.Bool("daemon", false, "run as background daemon (no interactive channel; cron only)")
	webFlag        = flag.Bool("web", false, "also start web dashboard alongside the agent")
	pruneMemories  = flag.Bool("prune-memories", false, "list memory entries that look like transcripts (long + markdown structure); pair with --confirm to delete them")
	pruneConfirm   = flag.Bool("confirm", false, "actually delete when used with --prune-memories (defaults to dry-run)")
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
	// "daimon mcp --help" does not trigger flag's unknown-flag error.
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

	if len(os.Args) > 1 && os.Args[1] == "costs" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runCostsCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "config" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runConfigCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "setup" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runSetupCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runDoctorCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "web" {
		cfgPath := extractFlagValue(os.Args[2:], "--config", "-config")
		if err := runWebCommand(os.Args[2:], cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "version" {
		if err := runVersionCommand(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "update" {
		if err := runUpdateCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("daimon %s (%s, %s)\n", version, commit, date)
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

	if *pruneMemories {
		if err := runPruneMemories(*cfgPath, *pruneConfirm); err != nil {
			fmt.Fprintf(os.Stderr, "prune-memories failed: %v\n", err)
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
				fmt.Fprintln(os.Stdout, "No config file found. Create one at ~/.daimon/config.yaml before running in non-interactive mode.")
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

	toolsRegistry := tool.BuildRegistrySimple(cfg.Tools)

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

	// Build skill index + budget check, create load_skill tool.
	autoloadSkills, skillIndex := agent.InitSkillInjection(skillContents, cfg.Agent.MaxContextTokens)
	skillMap := make(map[string]tool.SkillContent, len(skillContents))
	for _, s := range skillContents {
		skillMap[s.Name] = tool.SkillContent{Name: s.Name, Prose: s.Prose}
	}
	loadSkillTool := tool.NewSkillLoaderTool(skillMap)
	toolsRegistry[loadSkillTool.Name()] = loadSkillTool

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

	activeProv := config.ResolveActiveProvider(*cfg)
	prov, err := buildProvider(activeProv)
	if err != nil {
		slog.Error("failed to create provider", "error", err)
		os.Exit(1)
	}
	slog.Info("provider initialized", "name", prov.Name(), "configured_model", activeProv.Model)

	if cfg.Fallback != nil {
		fallbackProv, err := buildProvider(asProviderConfig(cfg.Fallback))
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

	// Warn if media is enabled but the backing store does not support it.
	// FileStore does not implement MediaStore; SQLiteStore does. Media ops will
	// be silently dropped by callers that type-assert before writing.
	if config.BoolVal(cfg.Media.Enabled) {
		if _, ok := st.(store.MediaStore); !ok {
			slog.Warn("media enabled but store backend does not support media — media will be dropped",
				"store_type", cfg.Store.Type)
		}
	}

	// Register context-mode tools (BatchExecTool, SearchOutputTool) if enabled
	if cfg.Agent.ContextMode.Mode != config.ContextModeOff {
		if outputStore, ok := st.(store.OutputStore); ok {
			batchTool := tool.NewBatchExecTool(outputStore, tool.BatchExecToolConfig{
				MaxOutputBytes: cfg.Agent.ContextMode.ShellMaxOutput * 2,
				Timeout:        cfg.Agent.ContextMode.SandboxTimeout,
			})
			searchTool := tool.NewSearchOutputTool(outputStore)

			// Add to registry (won't overwrite existing tools)
			if _, exists := toolsRegistry[batchTool.Name()]; !exists {
				toolsRegistry[batchTool.Name()] = batchTool
				slog.Info("context-mode: registered batch_exec tool")
			}
			if _, exists := toolsRegistry[searchTool.Name()]; !exists {
				toolsRegistry[searchTool.Name()] = searchTool
				slog.Info("context-mode: registered search_output tool")
			}
		} else {
			slog.Warn("context-mode: store does not implement OutputStore, context-mode tools not available")
		}
	}

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
			mediaStore, _ := st.(store.MediaStore)
			t, err := channel.NewTelegramChannel(cfg.Channel, cfg.Media, mediaStore)
			if err != nil {
				slog.Error("failed to initialize telegram channel", "error", err)
				os.Exit(1)
			}
			channels = append(channels, t)
		case "whatsapp":
			mediaStore, _ := st.(store.MediaStore)
			wa, err := channel.NewWhatsAppChannel(cfg.Channel, cfg.Media, mediaStore)
			if err != nil {
				slog.Error("failed to initialize whatsapp channel", "error", err)
				os.Exit(1)
			}
			channels = append(channels, wa)
		case "discord":
			mediaStore, _ := st.(store.MediaStore)
			d, err := channel.NewDiscordChannel(cfg.Channel, cfg.Media, mediaStore)
			if err != nil {
				slog.Error("failed to initialize discord channel", "error", err)
				os.Exit(1)
			}
			channels = append(channels, d)
		case "cli", "":
			mediaStore, _ := st.(store.MediaStore)
			channels = append(channels, channel.NewCLIChannelDefault(cfg.Channel, cfg.Media, mediaStore))
		default:
			slog.Error("unknown channel type", "type", cfg.Channel.Type)
			os.Exit(1)
		}
	}

	// Build cron subsystem if enabled.
	var cronScheduler cronpkg.SchedulerIface
	var concreteScheduler *cronpkg.Scheduler
	var cronChannelRef *cronpkg.CronChannel
	if cfg.Cron.Enabled {
		tz, _ := time.LoadLocation(cfg.Cron.Timezone)
		scheduler := cronpkg.NewScheduler(cronSt, tz, cfg.Cron.RetentionDays, cfg.Cron.MaxResultsPerJob)
		cronScheduler = scheduler
		concreteScheduler = scheduler

		// origSender delivers cron results back to the user's real channel.
		// It's nil-safe: if channels is empty (daemon mode), Send is skipped by CronChannel.
		var origSender cronpkg.OriginalSender
		if len(channels) > 0 {
			userCh := channels[0]
			origSender = userCh.Send
		}

		cronChannel := cronpkg.NewCronChannel(scheduler, cronSt, origSender, cfg.Cron.NotifyOnCompletion)
		cronChannelRef = cronChannel
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

	// Notification system (optional).
	var notifyBus notify.Bus
	if cfg.Notifications.Enabled && len(cfg.Notifications.Rules) > 0 {
		bus := notify.NewEventBus(
			cfg.Notifications.BusBufferSize,
			cfg.Notifications.MaxPerMinute,
			time.Duration(cfg.Notifications.HandlerTimeoutSec)*time.Second,
		)
		sender := notify.NewNotificationSender(mux, auditor, bus)
		engine, err := notify.NewRulesEngine(cfg.Notifications.Rules, sender)
		if err != nil {
			slog.Error("notifications: failed to create rules engine", "error", err)
		} else {
			bus.Subscribe(engine.Handle)
			for _, rule := range cfg.Notifications.Rules {
				if !notify.KnownEventTypes[rule.EventType] {
					slog.Warn("notify: rule references unknown event type", "rule", rule.Name, "event_type", rule.EventType)
				}
			}
			notifyBus = bus
			slog.Info("notifications enabled", "rules", len(cfg.Notifications.Rules))
		}
	}
	if concreteScheduler != nil {
		concreteScheduler.WithBus(notifyBus)
	}
	if cronChannelRef != nil {
		cronChannelRef.WithBus(notifyBus)
	}

	ag := agent.New(cfg.Agent, cfg.Limits, cfg.Filter, mux, prov, st, auditor, toolsRegistry, autoloadSkills, skillIndex, cfg.Cron.MaxConcurrent, config.BoolVal(activeProv.Stream)).
		WithBus(notifyBus).
		WithCronCommands(cronScheduler, cronSt)
	wireSmartMemory(ag, prov, st, cfg, toolsRegistry)
	ragWiring := wireRAG(cfg, st, prov, ag, toolsRegistry)
	if ragWiring.Worker != nil {
		ragWiring.Worker.Start(ctx)
		defer ragWiring.Worker.Stop()
	}

	// Start web dashboard if enabled in config or via --web flag.
	if cfg.Web.Enabled || *webFlag {
		// Ensure auth token is set — generate one if missing.
		if cfg.Web.AuthToken == "" {
			tok, err := web.GenerateToken()
			if err != nil {
				slog.Error("failed to generate web auth token", "error", err)
				os.Exit(1)
			}
			cfg.Web.AuthToken = tok
		}

		webCh := channel.NewWebChannel(cfg.Web.AllowedOrigins...)
		// Add WebChannel to the mux so the agent can receive/send web chat messages.
		channels = append(channels, webCh)
		mux = channel.NewMultiplexChannel(channels)
		// Rebuild the agent with the updated mux (WebChannel included).
		ag = agent.New(cfg.Agent, cfg.Limits, cfg.Filter, mux, prov, st, auditor, toolsRegistry, autoloadSkills, skillIndex, cfg.Cron.MaxConcurrent, config.BoolVal(activeProv.Stream)).
			WithBus(notifyBus).
			WithCronCommands(cronScheduler, cronSt)
		wireSmartMemory(ag, prov, st, cfg, toolsRegistry)
		// Re-wire RAG into the rebuilt agent (web path). Reuse the DocumentStore
		// + embed function built by wireRAG — do NOT construct a second store.
		if ragWiring.Worker != nil && ragWiring.Store != nil {
			ragCfg := cfg.RAG
			ag.WithRAGStore(ragWiring.Store, ragWiring.EmbedFn, ragCfg.TopK, ragCfg.MaxContextTokens)
		}
		// Re-wire metrics recorder into the rebuilt agent so the same ring buffer
		// is shared between the agent loop and the web handler.
		if ragWiring.Metrics != nil {
			ag.WithRAGMetrics(ragWiring.Metrics)
		}

		resolvedCfgPath, _ := config.FindConfigPath(*cfgPath)
		mcpSvc := mcp.NewMCPService(resolvedCfgPath)

		provRegistry := provider.NewStaticRegistry(*cfg)

		// Non-blocking startup validation: warn if the configured model is not
		// found in the provider's live model list. Never blocks startup on error.
		valCtx, valCancel := context.WithTimeout(ctx, 10*time.Second)
		_ = web.ValidateConfiguredModel(valCtx, provRegistry, *cfg)
		valCancel()

		mediaStore, _ := st.(store.MediaStore)
		webSrv := web.NewServer(web.ServerDeps{
			Store:            st,
			Auditor:          auditor,
			Config:           cfg,
			ConfigPath:       resolvedCfgPath,
			MCPService:       mcpSvc,
			ProviderRegistry: provRegistry,
			Tools:            toolsRegistry,
			StartedAt:        time.Now(),
			Version:          version,
			WebChannel:       webCh,
			MediaStore:       mediaStore,
			DocStore:         ragWiring.Store,
			IngestWorker:     ragWiring.Worker,
			RAGMetrics:       ragWiring.Metrics,
		})
		if err := webSrv.Start(); err != nil {
			slog.Error("failed to start web dashboard", "error", err)
		} else {
			tokenHint := cfg.Web.AuthToken
			if len(tokenHint) > 8 {
				tokenHint = tokenHint[:8] + "..."
			}
			slog.Info("web dashboard available",
				"url", fmt.Sprintf("http://%s:%d", cfg.Web.Host, cfg.Web.Port),
				"auth_token", tokenHint,
			)
			defer func() {
				shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutCancel()
				if err := webSrv.Shutdown(shutCtx); err != nil {
					slog.Error("web dashboard shutdown error", "error", err)
				}
			}()
		}
	}

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
// Empty type defaults to "anthropic" for backwards compatibility.
func buildProvider(cfg config.ProviderConfig) (provider.Provider, error) {
	if cfg.Type == "" {
		cfg.Type = "anthropic"
	}
	return provider.NewFromConfig(cfg)
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
