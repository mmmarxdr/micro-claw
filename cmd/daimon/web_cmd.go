package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"daimon/internal/agent"
	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	cronpkg "daimon/internal/cron"
	"daimon/internal/mcp"
	"daimon/internal/notify"
	"daimon/internal/provider"
	"daimon/internal/skill"
	"daimon/internal/store"
	"daimon/internal/tool"
	"daimon/internal/web"
)

// runWebCommand implements `daimon web` — starts the web dashboard with a
// full agent loop backed by a WebChannel. Users can chat at /ws/chat.
// Blocks until SIGINT/SIGTERM.
func runWebCommand(args []string, cfgPath string) error {
	// Subcommand: `daimon web token` — print the auth token from config.
	if len(args) > 0 && args[0] == "token" {
		return runWebTokenCommand(cfgPath)
	}

	fs := flag.NewFlagSet("web", flag.ExitOnError)
	port := fs.Int("port", 0, "override web dashboard port")
	host := fs.String("host", "", "override web dashboard host")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		if !errors.Is(err, config.ErrNoConfig) {
			return fmt.Errorf("web: failed to load config: %w", err)
		}
		// No config file — start in setup-only mode.
		cfg = &config.Config{}
		cfg.ApplyDefaults()
	}

	// Override from flags.
	if *port > 0 {
		cfg.Web.Port = *port
	}
	if *host != "" {
		cfg.Web.Host = *host
	}
	// Force-enable for this subcommand regardless of config setting.
	cfg.Web.Enabled = true

	// Ensure auth token is set — generate one if missing.
	if cfg.Web.AuthToken == "" {
		tok, err := web.GenerateToken()
		if err != nil {
			return fmt.Errorf("web: failed to generate auth token: %w", err)
		}
		cfg.Web.AuthToken = tok
	}

	configureLogging(cfg.Logging)

	// If provider is not configured, run in setup-only mode:
	// start the web server with only setup endpoints (no agent loop).
	ok, _ := config.IsProviderConfigured(*cfg)
	if !ok {
		return runWebSetupOnly(cfg, cfgPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- Store ----
	st, err := store.New(cfg.Store)
	if err != nil {
		return fmt.Errorf("web: failed to initialize store: %w", err)
	}
	defer st.Close()

	// ---- Auditor ----
	var aud audit.Auditor = audit.NoopAuditor{}
	if cfg.Audit.Enabled {
		switch cfg.Audit.Type {
		case "sqlite":
			sa, err := audit.NewSQLiteAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize sqlite auditor, using noop", "error", err)
			} else {
				aud = sa
				slog.Info("audit enabled", "type", "sqlite", "path", cfg.Audit.Path)
			}
		default:
			fa, err := audit.NewFileAuditor(cfg.Audit.Path)
			if err != nil {
				slog.Warn("failed to initialize file auditor, using noop", "error", err)
			} else {
				aud = fa
				slog.Info("audit enabled", "type", "file", "path", cfg.Audit.Path)
			}
		}
	}

	// ---- Tools ----
	toolsRegistry := tool.BuildRegistrySimple(cfg.Tools)

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

	autoloadSkills, skillIndex := agent.InitSkillInjection(skillContents, cfg.Agent.MaxContextTokens)
	skillMap := make(map[string]tool.SkillContent, len(skillContents))
	for _, s := range skillContents {
		skillMap[s.Name] = tool.SkillContent{Name: s.Name, Prose: s.Prose}
	}
	loadSkillTool := tool.NewSkillLoaderTool(skillMap)
	toolsRegistry[loadSkillTool.Name()] = loadSkillTool

	if cfg.Tools.MCP.Enabled {
		mcpTools, mcpManager, err := mcp.BuildMCPTools(ctx, cfg.Tools.MCP)
		if err != nil {
			return fmt.Errorf("web: failed to build MCP tools: %w", err)
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

	// ---- Provider ----
	activeProv := config.ResolveActiveProvider(*cfg)
	prov, err := buildProvider(activeProv)
	if err != nil {
		return fmt.Errorf("web: failed to create provider: %w", err)
	}
	slog.Info("provider initialized", "name", prov.Name())

	if cfg.Fallback != nil {
		fallbackProv, err := buildProvider(asProviderConfig(cfg.Fallback))
		if err != nil {
			return fmt.Errorf("web: failed to create fallback provider: %w", err)
		}
		if prov.SupportsTools() && !fallbackProv.SupportsTools() {
			slog.Warn("fallback provider does not support tools",
				"primary", prov.Name(), "fallback", fallbackProv.Name())
		}
		prov = provider.NewFallbackProvider(prov, fallbackProv, slog.Default())
	}

	hcCtx, hcCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hcCancel()
	verifiedName, err := prov.HealthCheck(hcCtx)
	if err != nil {
		return fmt.Errorf("web: provider health check failed: %w", err)
	}
	slog.Info("provider health check passed", "verified_model_name", verifiedName)

	// ---- Output-indexing tools (search_output + batch_exec) ----
	// These are independent of context_mode. They require an FTS5-capable
	// store (SQLite); on FileStore the OutputStore interface is implemented
	// as a no-op, which would make the tools dead weight — so we register
	// them only when the backing store actually persists output. The
	// bounded_exec sandbox that replaces native shell is still gated on
	// context_mode.Mode elsewhere in the loop; auto-indexing can run
	// without that sandbox.
	if cfg.Store.Type == "sqlite" {
		if outputStore, ok := st.(store.OutputStore); ok {
			batchCfg := tool.BatchExecToolConfig{
				MaxOutputBytes: cfg.Agent.ContextMode.ShellMaxOutput * 2,
				Timeout:        cfg.Agent.ContextMode.SandboxTimeout,
			}
			// In off mode the context-mode values are zero; fall back to
			// defaults that match the native shell cap (64 KB, step 2) so
			// batch_exec isn't silently more generous than shell_exec.
			if batchCfg.MaxOutputBytes == 0 {
				batchCfg.MaxOutputBytes = 64 * 1024
			}
			if batchCfg.Timeout == 0 {
				batchCfg.Timeout = 30 * time.Second
			}
			batchTool := tool.NewBatchExecTool(outputStore, batchCfg)
			searchTool := tool.NewSearchOutputTool(outputStore)
			if _, exists := toolsRegistry[batchTool.Name()]; !exists {
				toolsRegistry[batchTool.Name()] = batchTool
			}
			if _, exists := toolsRegistry[searchTool.Name()]; !exists {
				toolsRegistry[searchTool.Name()] = searchTool
			}
		}
	}

	// ---- Channels ----
	webCh := channel.NewWebChannel(cfg.Web.AllowedOrigins...)
	var channels []channel.Channel
	channels = append(channels, webCh)

	// Also start the configured messaging channel (Telegram, Discord, etc.)
	// so notifications and cron results can be delivered there.
	switch cfg.Channel.Type {
	case "telegram":
		mediaStore, _ := st.(store.MediaStore)
		tg, err := channel.NewTelegramChannel(cfg.Channel, cfg.Media, mediaStore)
		if err != nil {
			slog.Warn("web: failed to start telegram channel", "error", err)
		} else {
			channels = append(channels, tg)
			slog.Info("telegram channel started alongside web dashboard")
		}
	case "discord":
		mediaStore, _ := st.(store.MediaStore)
		d, err := channel.NewDiscordChannel(cfg.Channel, cfg.Media, mediaStore)
		if err != nil {
			slog.Warn("web: failed to start discord channel", "error", err)
		} else {
			channels = append(channels, d)
			slog.Info("discord channel started alongside web dashboard")
		}
	case "whatsapp":
		mediaStore, _ := st.(store.MediaStore)
		wa, err := channel.NewWhatsAppChannel(cfg.Channel, cfg.Media, mediaStore)
		if err != nil {
			slog.Warn("web: failed to start whatsapp channel", "error", err)
		} else {
			channels = append(channels, wa)
			slog.Info("whatsapp channel started alongside web dashboard")
		}
	}

	// ---- Cron (optional) ----
	var cronScheduler cronpkg.SchedulerIface
	var concreteScheduler *cronpkg.Scheduler
	var cronChannelRef *cronpkg.CronChannel
	var cronSt store.CronStore
	if cfg.Cron.Enabled {
		cs, ok := st.(store.CronStore)
		if !ok {
			return fmt.Errorf("web: cron requires sqlite store (set store.type = sqlite)")
		}
		cronSt = cs
		tz, _ := time.LoadLocation(cfg.Cron.Timezone)
		scheduler := cronpkg.NewScheduler(cs, tz, cfg.Cron.RetentionDays, cfg.Cron.MaxResultsPerJob)
		cronScheduler = scheduler
		concreteScheduler = scheduler

		cronChannel := cronpkg.NewCronChannel(scheduler, cs, webCh.Send, cfg.Cron.NotifyOnCompletion)
		cronChannelRef = cronChannel
		channels = append(channels, cronChannel)

		cronTools := tool.BuildCronTools(scheduler, cs, toolsRegistry, prov)
		for name, t := range cronTools {
			if _, exists := toolsRegistry[name]; !exists {
				toolsRegistry[name] = t
			}
		}
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
		sender := notify.NewNotificationSender(mux, aud, bus)
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

	ag := agent.New(
		cfg.Agent, cfg.Limits, cfg.Filter, mux, prov, st, aud,
		toolsRegistry, autoloadSkills, skillIndex,
		cfg.Cron.MaxConcurrent, config.BoolVal(activeProv.Stream),
	).WithBus(notifyBus).WithCronCommands(cronScheduler, cronSt)
	wireSmartMemory(ag, prov, st, cfg, toolsRegistry)
	ragWiring := wireRAG(cfg, st, prov, ag, toolsRegistry)
	if ragWiring.Worker != nil {
		ragWiring.Worker.Start(ctx)
		defer ragWiring.Worker.Stop()
	}

	// ---- Web server ----
	resolvedCfgPath, _ := config.FindConfigPath(cfgPath)
	mcpSvc := mcp.NewMCPService(resolvedCfgPath)

	provRegistry := provider.NewStaticRegistry(*cfg)

	mediaStore, _ := st.(store.MediaStore)
	srv := web.NewServer(web.ServerDeps{
		Store:            st,
		Auditor:          aud,
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

	if err := srv.Start(); err != nil {
		return fmt.Errorf("web: failed to start server: %w", err)
	}
	printDashboardBanner(cfg.Web.Host, cfg.Web.Port, cfg.Web.AuthToken)

	// Shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("web: shutting down")
		if err := ag.Shutdown(); err != nil {
			slog.Error("agent shutdown error", "error", err)
		}
		cancel()
	}()

	// Run agent loop (blocks until shutdown).
	if err := ag.Run(ctx); err != nil && err != context.Canceled {
		slog.Error("web: agent loop exited with error", "error", err)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	slog.Info("web dashboard shutting down")
	return srv.Shutdown(shutCtx)
}

// runWebSetupOnly starts the web server in setup-only mode when no provider is
// configured. Only setup endpoints are functional — no agent loop runs.
// Blocks until SIGINT/SIGTERM.
func runWebSetupOnly(cfg *config.Config, cfgPath string) error {
	slog.Info("no provider configured — starting in setup-only mode")

	st, err := store.New(cfg.Store)
	if err != nil {
		return fmt.Errorf("web setup: failed to initialize store: %w", err)
	}
	defer st.Close()

	resolvedCfgPath, _ := config.FindConfigPath(cfgPath)

	srv := web.NewServer(web.ServerDeps{
		Store:      st,
		Auditor:    audit.NoopAuditor{},
		Config:     cfg,
		ConfigPath: resolvedCfgPath,
		StartedAt:  time.Now(),
		Version:    version,
	})

	if err := srv.Start(); err != nil {
		return fmt.Errorf("web setup: failed to start server: %w", err)
	}
	printSetupBanner(cfg.Web.Host, cfg.Web.Port)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("web setup: shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	return srv.Shutdown(shutCtx)
}

// printDashboardBanner prints a visible box with the dashboard URL and auth token.
func printDashboardBanner(host string, port int, token string) {
	url := fmt.Sprintf("http://%s:%d", host, port)
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────┐")
	fmt.Println("  │                                                 │")
	fmt.Printf("  │  ⚡ Dashboard: %-33s│\n", url)
	fmt.Printf("  │  🔑 Token:     %-33s│\n", token[:16]+"...")
	fmt.Println("  │                                                 │")
	fmt.Println("  │  Token saved in config. Retrieve anytime with:  │")
	fmt.Println("  │  daimon web token                           │")
	fmt.Println("  │                                                 │")
	fmt.Println("  └─────────────────────────────────────────────────┘")
	fmt.Println()
}

// printSetupBanner prints a visible box for setup-only mode.
func printSetupBanner(host string, port int) {
	url := fmt.Sprintf("http://%s:%d", host, port)
	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────┐")
	fmt.Println("  │                                                 │")
	fmt.Printf("  │  ⚡ Setup wizard: %-30s│\n", url)
	fmt.Println("  │                                                 │")
	fmt.Println("  │  No config found. Open the URL above to         │")
	fmt.Println("  │  configure your agent.                          │")
	fmt.Println("  │                                                 │")
	fmt.Println("  └─────────────────────────────────────────────────┘")
	fmt.Println()
}

// runWebTokenCommand prints the auth token from the config file.
func runWebTokenCommand(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("web token: %w", err)
	}
	if cfg.Web.AuthToken == "" {
		fmt.Println("No auth token configured. Start the dashboard to generate one:")
		fmt.Println("  daimon web")
		return nil
	}
	fmt.Println(cfg.Web.AuthToken)
	return nil
}
