package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"microagent/internal/agent"
	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	cronpkg "microagent/internal/cron"
	"microagent/internal/mcp"
	"microagent/internal/provider"
	"microagent/internal/skill"
	"microagent/internal/store"
	"microagent/internal/tool"
	"microagent/internal/web"
)

// runWebCommand implements `microagent web` — starts the web dashboard with a
// full agent loop backed by a WebChannel. Users can chat at /ws/chat.
// Blocks until SIGINT/SIGTERM.
func runWebCommand(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	port := fs.Int("port", 0, "override web dashboard port")
	host := fs.String("host", "", "override web dashboard host")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("web: failed to load config: %w", err)
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
	prov, err := buildProvider(cfg.Provider)
	if err != nil {
		return fmt.Errorf("web: failed to create provider: %w", err)
	}
	slog.Info("provider initialized", "name", prov.Name())

	if cfg.Provider.Fallback != nil {
		fallbackProv, err := buildProvider(asProviderConfig(cfg.Provider.Fallback))
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

	// ---- Context-mode tools ----
	if cfg.Agent.ContextMode.Mode != config.ContextModeOff {
		if outputStore, ok := st.(store.OutputStore); ok {
			batchTool := tool.NewBatchExecTool(outputStore, tool.BatchExecToolConfig{
				MaxOutputBytes: cfg.Agent.ContextMode.ShellMaxOutput * 2,
				Timeout:        cfg.Agent.ContextMode.SandboxTimeout,
			})
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
	webCh := channel.NewWebChannel()
	var channels []channel.Channel
	channels = append(channels, webCh)

	// ---- Cron (optional) ----
	var cronScheduler cronpkg.SchedulerIface
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

		cronChannel := cronpkg.NewCronChannel(scheduler, cs, webCh.Send, cfg.Cron.NotifyOnCompletion)
		channels = append(channels, cronChannel)

		cronTools := tool.BuildCronTools(scheduler, cs, toolsRegistry, prov)
		for name, t := range cronTools {
			if _, exists := toolsRegistry[name]; !exists {
				toolsRegistry[name] = t
			}
		}
	}

	mux := channel.NewMultiplexChannel(channels)

	ag := agent.New(
		cfg.Agent, cfg.Limits, cfg.Filter, mux, prov, st, aud,
		toolsRegistry, autoloadSkills, skillIndex,
		cfg.Cron.MaxConcurrent, config.BoolVal(cfg.Provider.Stream),
	).WithCronCommands(cronScheduler, cronSt)

	// ---- Web server ----
	var ml provider.ModelLister
	if lister, ok := prov.(provider.ModelLister); ok {
		ml = lister
	}

	resolvedCfgPath, _ := config.FindConfigPath(cfgPath)
	mcpSvc := mcp.NewMCPService(resolvedCfgPath)

	srv := web.NewServer(web.ServerDeps{
		Store:       st,
		Auditor:     aud,
		Config:      cfg,
		MCPService:  mcpSvc,
		ModelLister: ml,
		Tools:       toolsRegistry,
		StartedAt:   time.Now(),
		Version:     version,
		WebChannel:  webCh,
	})

	if err := srv.Start(); err != nil {
		return fmt.Errorf("web: failed to start server: %w", err)
	}
	slog.Info("web dashboard available",
		"url", fmt.Sprintf("http://%s:%d", cfg.Web.Host, cfg.Web.Port),
		"auth_token", cfg.Web.AuthToken,
	)

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
