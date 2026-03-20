package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"microagent/internal/config"
	"microagent/internal/store"
)

// runCronCommand dispatches to the appropriate cron subcommand handler.
// args is os.Args[2:] (everything after "cron").
// cfgPath is the resolved --config value (may be empty).
func runCronCommand(args []string, cfgPath string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: microagent cron <list|delete|info>")
	}
	switch args[0] {
	case "list":
		return cronList(args[1:], cfgPath)
	case "delete", "rm":
		return cronDelete(args[1:], cfgPath)
	case "info":
		return cronInfo(args[1:], cfgPath)
	case "--help", "-help", "-h":
		fmt.Println("Usage: microagent cron <list|delete|info>")
		return nil
	default:
		return fmt.Errorf("unknown cron subcommand: %q\nUsage: microagent cron <list|delete|info>", args[0])
	}
}

// openCronStore opens a SQLiteStore (which implements CronStore).
// Returns the CronStore, a close function, and any error.
func openCronStore(cfgPath string) (store.CronStore, func(), error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Store.Type != "sqlite" {
		return nil, nil, errors.New("cron commands require store.type = sqlite")
	}
	s, err := store.NewSQLiteStore(cfg.Store)
	if err != nil {
		return nil, nil, err
	}
	// SQLiteStore implements CronStore; compile-time assertion in sqlitestore.go ensures this.
	return s, func() { s.Close() }, nil
}

// cronList implements `microagent cron list`.
func cronList(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("cron list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cs, closeFn, err := openCronStore(cfgPath)
	if err != nil {
		return fmt.Errorf("cron list: %w", err)
	}
	defer closeFn()

	jobs, err := cs.ListJobs(context.Background())
	if err != nil {
		return fmt.Errorf("cron list: %w", err)
	}

	if len(jobs) == 0 {
		fmt.Println("No cron jobs scheduled.")
		return nil
	}

	// Dynamic column widths.
	idW, schedW, nextW, promptW := 12, 8, 8, 6
	for _, j := range jobs {
		id12 := j.ID
		if len(id12) > 12 {
			id12 = id12[:12]
		}
		if len(id12) > idW {
			idW = len(id12)
		}
		if len(j.Schedule) > schedW {
			schedW = len(j.Schedule)
		}
		nextStr := "—"
		if j.NextRunAt != nil {
			nextStr = j.NextRunAt.Format(time.RFC1123)
		}
		if len(nextStr) > nextW {
			nextW = len(nextStr)
		}
		prompt := j.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		if len(prompt) > promptW {
			promptW = len(prompt)
		}
	}

	fmtRow := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%s\n", idW, schedW, nextW)
	fmt.Printf(fmtRow, "ID", "SCHEDULE", "NEXT RUN", "PROMPT")
	for _, j := range jobs {
		id12 := j.ID
		if len(id12) > 12 {
			id12 = id12[:12]
		}
		nextStr := "—"
		if j.NextRunAt != nil {
			nextStr = j.NextRunAt.Format(time.RFC1123)
		}
		prompt := j.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		fmt.Printf(fmtRow, id12, j.Schedule, nextStr, prompt)
	}

	return nil
}

// cronDelete implements `microagent cron delete <id> [--yes]`.
func cronDelete(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("cron delete", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip confirmation prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent cron delete <id> [--yes]")
		return fmt.Errorf("usage: microagent cron delete <id> [--yes]")
	}
	id := fs.Arg(0)

	if !*yes {
		if !isTTY(os.Stdin) {
			fmt.Fprintln(os.Stderr, "Error: --yes required when stdin is not a terminal")
			return fmt.Errorf("--yes required when stdin is not a terminal")
		}
		fmt.Printf("Delete cron job %q? [y/N]: ", id)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		answer := strings.TrimSpace(scanner.Text())
		if answer != "y" && answer != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	cs, closeFn, err := openCronStore(cfgPath)
	if err != nil {
		return fmt.Errorf("cron delete: %w", err)
	}
	defer closeFn()

	if err := cs.DeleteJob(context.Background(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("cron job %q not found", id)
		}
		return fmt.Errorf("cron delete: %w", err)
	}

	fmt.Printf("Deleted cron job %q.\n", id)
	return nil
}

// cronInfo implements `microagent cron info <id>`.
func cronInfo(args []string, cfgPath string) error {
	fs := flag.NewFlagSet("cron info", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: microagent cron info <id>")
		return fmt.Errorf("usage: microagent cron info <id>")
	}
	id := fs.Arg(0)

	cs, closeFn, err := openCronStore(cfgPath)
	if err != nil {
		return fmt.Errorf("cron info: %w", err)
	}
	defer closeFn()

	ctx := context.Background()

	job, err := cs.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("cron job %q not found", id)
		}
		return fmt.Errorf("cron info: %w", err)
	}

	// Print job fields.
	fmt.Printf("ID:             %s\n", job.ID)
	fmt.Printf("Schedule:       %s\n", job.Schedule)
	fmt.Printf("Schedule (human): %s\n", job.ScheduleHuman)
	fmt.Printf("Prompt:         %s\n", job.Prompt)
	fmt.Printf("Channel ID:     %s\n", job.ChannelID)
	fmt.Printf("Enabled:        %v\n", job.Enabled)
	fmt.Printf("Created At:     %s\n", job.CreatedAt.Format(time.RFC1123))
	if job.LastRunAt != nil {
		fmt.Printf("Last Run At:    %s\n", job.LastRunAt.Format(time.RFC1123))
	} else {
		fmt.Printf("Last Run At:    —\n")
	}
	if job.NextRunAt != nil {
		fmt.Printf("Next Run At:    %s\n", job.NextRunAt.Format(time.RFC1123))
	} else {
		fmt.Printf("Next Run At:    —\n")
	}

	// Print recent results.
	fmt.Println()
	fmt.Println("--- Recent Results (last 10) ---")
	results, err := cs.ListResults(ctx, id, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load results: %v\n", err)
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No results recorded yet.")
		return nil
	}

	for _, r := range results {
		fmt.Printf("[%s]\n", r.RanAt.Format(time.RFC1123))
		if r.ErrorMsg != "" {
			fmt.Printf("  Error: %s\n", r.ErrorMsg)
		}
		out := r.Output
		if len(out) > 200 {
			out = out[:200] + "..."
		}
		if out != "" {
			fmt.Printf("  Output: %s\n", out)
		}
		fmt.Println()
	}

	return nil
}
