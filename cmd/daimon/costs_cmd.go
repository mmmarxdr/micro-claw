package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"daimon/internal/config"
	"daimon/internal/cost"
	"daimon/internal/store"
)

// runCostsCommand dispatches to the appropriate costs subcommand handler.
// args is os.Args[2:] (everything after "costs").
func runCostsCommand(args []string, cfgPath string) error {
	if len(args) == 0 {
		return costsSummary(args, cfgPath) // default subcommand
	}
	switch args[0] {
	case "summary":
		return costsSummary(args[1:], cfgPath)
	case "by-model":
		return costsByModel(args[1:], cfgPath)
	case "list-models":
		return costsListModels(args[1:], cfgPath)
	case "--help", "-help", "-h":
		fmt.Println("Usage: microagent costs [summary|by-model|list-models]")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  summary     Show aggregate cost summary (default)")
		fmt.Println("  by-model    Show per-model cost breakdown")
		fmt.Println("  list-models Show supported model pricing table")
		return nil
	default:
		// Assume it's a flag for summary (e.g., --days 7)
		return costsSummary(args, cfgPath)
	}
}

// openCostStore opens a SQLiteStore (which implements CostStore).
func openCostStore(cfgPath string) (store.CostStore, func(), error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Store.Type != "sqlite" {
		return nil, nil, errors.New("cost commands require store.type = sqlite")
	}
	s, err := store.NewSQLiteStore(cfg.Store)
	if err != nil {
		return nil, nil, err
	}
	return s, func() { s.Close() }, nil
}

// costsSummary implements `microagent costs [summary]`.
func costsSummary(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("costs summary", flag.ContinueOnError)
	days := fs.Int("days", 0, "limit to last N days (0 = all time)")
	model := fs.String("model", "", "filter by model name (substring match)")
	channel := fs.String("channel", "", "filter by channel ID")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cs, closeFn, err := openCostStore(cfgPath)
	if err != nil {
		return fmt.Errorf("costs summary: %w", err)
	}
	defer closeFn()

	filter := store.CostFilter{
		ChannelID: *channel,
		Model:     *model,
	}
	if *days > 0 {
		filter.Since = time.Now().AddDate(0, 0, -*days)
	}

	summary, err := cs.GetCostSummary(context.Background(), filter)
	if err != nil {
		return fmt.Errorf("costs summary: %w", err)
	}

	if summary.RecordCount == 0 {
		fmt.Println("No cost records found.")
		return nil
	}

	fmt.Printf("Cost Summary")
	if *days > 0 {
		fmt.Printf(" (last %d days)", *days)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("─", 40))
	fmt.Printf("  Total calls:        %d\n", summary.RecordCount)
	fmt.Printf("  Input tokens:       %s\n", formatTokenCount(summary.TotalInputTokens))
	fmt.Printf("  Output tokens:      %s\n", formatTokenCount(summary.TotalOutputTokens))
	fmt.Printf("  Total cost:         %s\n", cost.FormatCost(summary.TotalCostUSD))

	if len(summary.ByModel) > 0 {
		fmt.Println()
		fmt.Println("Per Model:")
		fmt.Printf("  %-30s %8s %10s %10s\n", "MODEL", "CALLS", "TOKENS", "COST")
		for _, m := range summary.ByModel {
			totalTokens := m.InputTokens + m.OutputTokens
			name := m.Model
			if len(name) > 30 {
				name = name[:27] + "..."
			}
			fmt.Printf("  %-30s %8d %10s %10s\n",
				name,
				m.CallCount,
				formatTokenCount(totalTokens),
				cost.FormatCost(m.TotalCostUSD),
			)
		}
	}

	return nil
}

// costsByModel implements `microagent costs by-model`.
func costsByModel(args []string, cfgPath string) error {
	// Same as summary but focuses on the per-model table.
	// Accepts the same flags for filtering.
	return costsSummary(args, cfgPath)
}

// costsListModels implements `microagent costs list-models`.
func costsListModels(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("costs list-models", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	models := cost.All()
	if len(models) == 0 {
		fmt.Println("No models configured.")
		return nil
	}

	fmt.Printf("Supported Models (%d):\n", len(models))
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  %-30s %12s %12s\n", "MODEL", "INPUT $/1K", "OUTPUT $/1K")
	for name, p := range models {
		inputPer1K := p.Input * 1000
		outputPer1K := p.Output * 1000
		fmt.Printf("  %-30s $%11.6f $%11.6f\n", name, inputPer1K, outputPer1K)
	}

	return nil
}

// formatTokenCount formats large numbers with comma separators.
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
}
