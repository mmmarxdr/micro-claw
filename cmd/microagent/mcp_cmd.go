package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"microagent/internal/config"
	"microagent/internal/mcp"
	"microagent/internal/tui"
)

// multiFlag implements flag.Value for repeatable --env KEY=VALUE flags.
type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ", ")
}

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// runMCPCommand dispatches to the appropriate mcp subcommand handler.
// args is os.Args[2:] (everything after "mcp").
// cfgPath is the resolved --config value (may be empty → config.FindConfigPath uses default search).
func runMCPCommand(args []string, cfgPath string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: microagent mcp <list|add|remove|test|validate|manage>")
	}
	switch args[0] {
	case "list":
		return mcpList(args[1:], cfgPath)
	case "add":
		return mcpAdd(args[1:], cfgPath)
	case "remove", "rm":
		return mcpRemove(args[1:], cfgPath)
	case "test":
		return mcpTest(args[1:], cfgPath)
	case "validate":
		return mcpValidate(args[1:], cfgPath)
	case "manage":
		return mcpManage(args[1:], cfgPath)
	case "--help", "-help", "-h":
		fmt.Println("Usage: microagent mcp <list|add|remove|test|validate|manage>")
		return nil
	default:
		return fmt.Errorf("unknown mcp subcommand: %q\nUsage: microagent mcp <list|add|remove|test|validate|manage>", args[0])
	}
}

// resolveCfgPath resolves the config path, returning an error if not found.
func resolveCfgPath(cfgPath string) (string, error) {
	resolved, err := config.FindConfigPath(cfgPath)
	if err != nil {
		if errors.Is(err, config.ErrNoConfig) {
			return "", fmt.Errorf("no config file found; create one at ~/.microagent/config.yaml or pass --config")
		}
		return "", fmt.Errorf("config: %w", err)
	}
	return resolved, nil
}

// mcpList implements `microagent mcp list`.
func mcpList(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	svc := mcp.NewMCPService(resolved)
	statuses, err := svc.List(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing MCP servers: %v\n", err)
		return err
	}

	if len(statuses) == 0 {
		fmt.Println("No MCP servers configured. Use 'microagent mcp add' to add one.")
		return nil
	}

	if *jsonOut {
		cfgs := make([]config.MCPServerConfig, len(statuses))
		for i, s := range statuses {
			cfgs[i] = s.Config
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cfgs)
	}

	// Build table with dynamic column widths.
	nameW, transW, cmdW := 4, 9, 12 // minimum header widths
	for _, s := range statuses {
		if len(s.Config.Name) > nameW {
			nameW = len(s.Config.Name)
		}
		if len(s.Config.Transport) > transW {
			transW = len(s.Config.Transport)
		}
		var cmdURL string
		switch s.Config.Transport {
		case "stdio":
			cmdURL = strings.Join(s.Config.Command, " ")
		case "http":
			cmdURL = s.Config.URL
		}
		if len(cmdURL) > cmdW {
			cmdW = len(cmdURL)
		}
	}

	fmtRow := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", nameW, transW, cmdW)
	fmt.Printf(fmtRow, "NAME", "TRANSPORT", "COMMAND / URL", "PREFIX")
	for _, s := range statuses {
		var cmdURL string
		switch s.Config.Transport {
		case "stdio":
			cmdURL = strings.Join(s.Config.Command, " ")
		case "http":
			cmdURL = s.Config.URL
		}
		prefix := "no"
		if s.Config.PrefixTools {
			prefix = "yes"
		}
		fmt.Printf(fmtRow, s.Config.Name, s.Config.Transport, cmdURL, prefix)
	}
	return nil
}

// mcpAdd implements `microagent mcp add`.
func mcpAdd(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	name := fs.String("name", "", "server name (required)")
	transport := fs.String("transport", "stdio", "transport type: stdio|http")
	command := fs.String("command", "", "command to run (stdio only); split on whitespace")
	url := fs.String("url", "", "server URL (http only)")
	prefixTools := fs.Bool("prefix-tools", false, "prefix tool names with server name")
	noTest := fs.Bool("no-test", false, "suppress post-add test suggestion")
	envPairs := &multiFlag{}
	fs.Var(envPairs, "env", "environment variable (KEY=VALUE); repeatable")

	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	cfg := config.MCPServerConfig{
		Name:        *name,
		Transport:   *transport,
		URL:         *url,
		PrefixTools: *prefixTools,
	}

	if *command != "" {
		cfg.Command = strings.Fields(*command)
	}

	if len(*envPairs) > 0 {
		cfg.Env = make(map[string]string, len(*envPairs))
		for _, pair := range *envPairs {
			k, v, ok := strings.Cut(pair, "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: --env value %q must be in KEY=VALUE format\n", pair)
				return fmt.Errorf("--env value %q must be in KEY=VALUE format", pair)
			}
			cfg.Env[k] = v
		}
	}

	svc := mcp.NewMCPService(resolved)
	if err := svc.Add(context.Background(), cfg); err != nil {
		if errors.Is(err, mcp.ErrDuplicateName) {
			fmt.Fprintf(os.Stderr, "Error: server %q already exists\n", *name)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return err
	}

	if *noTest {
		fmt.Printf("Server %q added to config.\n", *name)
	} else {
		fmt.Printf("Server %q added to config. Run 'microagent mcp test %s' to verify.\n", *name, *name)
	}
	return nil
}

// mcpRemove implements `microagent mcp remove NAME`.
func mcpRemove(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("mcp remove", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip confirmation prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent mcp remove NAME [--yes]")
		return fmt.Errorf("usage: microagent mcp remove NAME [--yes]")
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
		fmt.Printf("Remove server %q? [y/N]: ", name)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		answer := strings.TrimSpace(scanner.Text())
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	svc := mcp.NewMCPService(resolved)
	if err := svc.Remove(context.Background(), name); err != nil {
		if errors.Is(err, mcp.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "Error: server %q not found\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		return err
	}

	fmt.Printf("Server %q removed.\n", name)
	return nil
}

// mcpTest implements `microagent mcp test NAME`.
func mcpTest(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("mcp test", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 15*time.Second, "connection timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent mcp test NAME [--timeout DURATION]")
		return fmt.Errorf("usage: microagent mcp test NAME [--timeout DURATION]")
	}
	name := fs.Arg(0)

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	// Load raw YAML to find the server config (preserve ${VAR} unexpanded).
	raw, err := os.ReadFile(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return fmt.Errorf("read config: %w", err)
	}

	var fullCfg config.Config
	if err := yaml.Unmarshal(raw, &fullCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %v\n", err)
		return fmt.Errorf("parse config: %w", err)
	}

	var serverCfg *config.MCPServerConfig
	for i, srv := range fullCfg.Tools.MCP.Servers {
		if srv.Name == name {
			serverCfg = &fullCfg.Tools.MCP.Servers[i]
			break
		}
	}
	if serverCfg == nil {
		fmt.Fprintf(os.Stderr, "Error: server %q not found in config\n", name)
		return fmt.Errorf("server %q not found in config", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	svc := mcp.NewMCPService(resolved)
	tools, err := svc.Test(ctx, *serverCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error testing %q: %v\n", name, err)
		return fmt.Errorf("test %q: %w", name, err)
	}

	if len(tools) == 0 {
		fmt.Printf("Connected to %q (%s). Discovered 0 tools (server registered no tools).\n",
			name, serverCfg.Transport)
		return nil
	}

	fmt.Printf("Connected to %q (%s). Discovered %d tools:\n", name, serverCfg.Transport, len(tools))
	for _, t := range tools {
		fmt.Printf("  %s\n", t)
	}
	return nil
}

// mcpValidate implements `microagent mcp validate`.
func mcpValidate(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("mcp validate", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved, err := resolveCfgPath(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return err
	}

	// Load raw YAML to avoid expanding ${VAR} during validation.
	raw, err := os.ReadFile(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		return fmt.Errorf("read config: %w", err)
	}

	var fullCfg config.Config
	if err := yaml.Unmarshal(raw, &fullCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %v\n", err)
		return fmt.Errorf("parse config: %w", err)
	}

	if !fullCfg.Tools.MCP.Enabled {
		fmt.Println("MCP is disabled (tools.mcp.enabled: false). Nothing to validate.")
		return nil
	}

	servers := fullCfg.Tools.MCP.Servers
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured.")
		return nil
	}

	svc := mcp.NewMCPService(resolved)
	hasError := false

	for _, srv := range servers {
		// Structural validation.
		if err := svc.Validate(srv); err != nil {
			fmt.Printf("ERROR: server %q: %v\n", srv.Name, err)
			hasError = true
			continue
		}
		fmt.Printf("OK:    server %q\n", srv.Name)

		// Warn about unset env vars.
		for k, v := range srv.Env {
			if _, err := config.ExpandSafeEnv(v); err != nil {
				fmt.Printf("WARN:  server %q: env var for key %q is not set (%v)\n", srv.Name, k, err)
			}
		}
		// Check URL for ${VAR} references.
		if srv.URL != "" {
			if _, err := config.ExpandSafeEnv(srv.URL); err != nil {
				fmt.Printf("WARN:  server %q: url contains unset env var (%v)\n", srv.Name, err)
			}
		}

		// Warn if stdio command[0] not on PATH.
		if srv.Transport == "stdio" && len(srv.Command) > 0 {
			if _, lookErr := exec.LookPath(srv.Command[0]); lookErr != nil {
				fmt.Printf("WARN:  server %q: command %q not found on PATH\n", srv.Name, srv.Command[0])
			}
		}
	}

	if hasError {
		return fmt.Errorf("validation failed")
	}
	fmt.Printf("All %d MCP server configs are valid.\n", len(servers))
	return nil
}

// mcpManage implements `microagent mcp manage` — launches the TUI management screen.
func mcpManage(_ []string, cfgPath string) error {
	return tui.RunMCPManage(cfgPath)
}
