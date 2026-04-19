package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	robfigcron "github.com/robfig/cron/v3"

	"daimon/internal/cron"
	"daimon/internal/store"
)

// WithCronCommands registers cron-specific slash commands on the agent when
// cron is enabled. Call before Run(). Both scheduler and cronStore must be non-nil.
func (a *Agent) WithCronCommands(scheduler cron.SchedulerIface, cronStore store.CronStore) *Agent {
	if scheduler != nil && cronStore != nil {
		registerCronCommands(a.commands, scheduler, cronStore)
	}
	return a
}

// registerCronCommands registers cron-specific slash commands on the registry.
// scheduler and cronStore are captured in closures; CommandContext is not expanded.
func registerCronCommands(reg *CommandRegistry, scheduler cron.SchedulerIface, cronStore store.CronStore) {
	reg.Register("tasks", "List active scheduled tasks", makeCmdTasks(scheduler))
	reg.Register("cancel", "Cancel a scheduled task: /cancel <task-id>", makeCmdCancel(cronStore, scheduler))
	reg.Register("cancel-confirm", "Confirm task cancellation: /cancel-confirm <task-id>", makeCmdCancelConfirm(cronStore, scheduler))
	reg.Register("schedule", "Schedule a new task: /schedule <min> <hour> <day> <month> <weekday> <prompt>", makeCmdSchedule(cronStore, scheduler))
	reg.Register("history", "Show task run history: /history <id> [limit]", makeCmdHistory(cronStore))
}

// resolveJobID finds a CronJob by exact ID or unambiguous prefix.
func resolveJobID(jobs []store.CronJob, prefix string) (store.CronJob, error) {
	// 1. Exact match first.
	for _, j := range jobs {
		if j.ID == prefix {
			return j, nil
		}
	}
	// 2. Prefix match.
	var matches []store.CronJob
	for _, j := range jobs {
		if strings.HasPrefix(j.ID, prefix) {
			matches = append(matches, j)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return store.CronJob{}, fmt.Errorf("ambiguous ID prefix %q matches %d tasks", prefix, len(matches))
	}
	return store.CronJob{}, fmt.Errorf("no task found with ID %q", prefix)
}

// validateCronExpr validates a cron expression using the robfig parser (5-field).
func validateCronExprCmd(expr string) error {
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	_, err := parser.Parse(strings.TrimSpace(expr))
	return err
}

// nextRunTimeCmd computes the next run time for a cron expression.
func nextRunTimeCmd(expr string, from time.Time) (time.Time, error) {
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	sched, err := parser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

// shortID returns the first 8 characters of a UUID-style ID.
// Safe when len(id) >= 8.
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// --------------------------------------------------------------------------
// /tasks — list active scheduled tasks
// --------------------------------------------------------------------------

func makeCmdTasks(scheduler cron.SchedulerIface) CommandHandler {
	return func(cc CommandContext) error {
		activeJobs, err := scheduler.ListActiveJobs(cc.Ctx)
		if err != nil {
			return fmt.Errorf("failed to list active jobs: %w", err)
		}
		if len(activeJobs) == 0 {
			cc.Reply("No scheduled tasks.")
			return nil
		}
		var sb strings.Builder
		sb.WriteString("Scheduled tasks:\n\n")
		for _, aj := range activeJobs {
			desc := aj.Job.Description
			if desc == "" {
				desc = aj.Job.Prompt
			}
			if len(desc) > 60 {
				desc = desc[:57] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s  %s\n", shortID(aj.Job.ID), aj.Job.Schedule))
			sb.WriteString(fmt.Sprintf("    %s\n", desc))
			if !aj.NextRun.IsZero() {
				sb.WriteString(fmt.Sprintf("    Next: %s\n", aj.NextRun.Format("2006-01-02 15:04")))
			}
			sb.WriteString("\n")
		}
		cc.Reply(sb.String())
		return nil
	}
}

// --------------------------------------------------------------------------
// /cancel — show task details and prompt for confirmation
// --------------------------------------------------------------------------

func makeCmdCancel(cronStore store.CronStore, _ cron.SchedulerIface) CommandHandler {
	return func(cc CommandContext) error {
		if strings.TrimSpace(cc.Args) == "" {
			cc.Reply("Usage: /cancel <task-id>")
			return nil
		}
		jobs, err := cronStore.ListJobs(cc.Ctx)
		if err != nil {
			return fmt.Errorf("failed to list jobs: %w", err)
		}
		job, err := resolveJobID(jobs, strings.TrimSpace(cc.Args))
		if err != nil {
			cc.Reply(err.Error())
			return nil
		}
		prompt := job.Prompt
		if len(prompt) > 100 {
			prompt = prompt[:97] + "..."
		}
		cc.Reply(fmt.Sprintf(
			"Task %s:\n  Schedule: %s\n  Prompt: %s\n\nTo confirm cancellation, type:\n  /cancel-confirm %s",
			shortID(job.ID), job.Schedule, prompt, shortID(job.ID),
		))
		return nil
	}
}

// --------------------------------------------------------------------------
// /cancel-confirm — actually remove the task
// --------------------------------------------------------------------------

func makeCmdCancelConfirm(cronStore store.CronStore, scheduler cron.SchedulerIface) CommandHandler {
	return func(cc CommandContext) error {
		if strings.TrimSpace(cc.Args) == "" {
			cc.Reply("Usage: /cancel-confirm <task-id>")
			return nil
		}
		jobs, err := cronStore.ListJobs(cc.Ctx)
		if err != nil {
			return fmt.Errorf("failed to list jobs: %w", err)
		}
		job, err := resolveJobID(jobs, strings.TrimSpace(cc.Args))
		if err != nil {
			cc.Reply(err.Error())
			return nil
		}
		// Remove from scheduler (best-effort — job may not be active).
		_ = scheduler.RemoveJob(cc.Ctx, job.ID)
		// Delete from store.
		if err := cronStore.DeleteJob(cc.Ctx, job.ID); err != nil {
			return fmt.Errorf("failed to delete task: %w", err)
		}
		cc.Reply(fmt.Sprintf("Task %s cancelled.", shortID(job.ID)))
		return nil
	}
}

// --------------------------------------------------------------------------
// /schedule — create a new scheduled task from chat
// --------------------------------------------------------------------------

func makeCmdSchedule(cronStore store.CronStore, scheduler cron.SchedulerIface) CommandHandler {
	return func(cc CommandContext) error {
		fields := strings.Fields(cc.Args)
		if len(fields) < 6 {
			cc.Reply("Usage: /schedule <min> <hour> <day> <month> <weekday> <prompt>\nExample: /schedule 0 9 * * * Good morning summary")
			return nil
		}
		cronExpr := strings.Join(fields[:5], " ")
		prompt := strings.Join(fields[5:], " ")

		// Validate cron expression.
		if err := validateCronExprCmd(cronExpr); err != nil {
			cc.Reply(fmt.Sprintf("Invalid cron expression %q: %v", cronExpr, err))
			return nil
		}

		// Compute next run time.
		nextRun, err := nextRunTimeCmd(cronExpr, time.Now())
		if err != nil {
			cc.Reply(fmt.Sprintf("Could not compute next run time: %v", err))
			return nil
		}

		// Build description from prompt (truncated).
		desc := prompt
		if len(desc) > 100 {
			desc = desc[:100]
		}

		// Persist the job (mirrors tool/cron.go pattern exactly).
		jobID := uuid.New().String()
		job := store.CronJob{
			ID:          jobID,
			Schedule:    cronExpr,
			Prompt:      prompt,
			ChannelID:   cc.ChannelID,
			Description: desc,
			Enabled:     true,
			CreatedAt:   time.Now(),
			NextRunAt:   &nextRun,
		}

		createdJob, err := cronStore.CreateJob(cc.Ctx, job)
		if err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}

		// Register with scheduler; roll back on failure.
		if err := scheduler.AddJob(cc.Ctx, createdJob); err != nil {
			_ = cronStore.DeleteJob(cc.Ctx, createdJob.ID)
			cc.Reply("Invalid schedule: " + err.Error())
			return nil
		}

		cc.Reply(fmt.Sprintf(
			"Scheduled: %q\nRuns: %s\nNext: %s\nID: %s",
			prompt,
			cronExpr,
			nextRun.Format("2006-01-02 15:04"),
			shortID(createdJob.ID),
		))
		return nil
	}
}

// --------------------------------------------------------------------------
// /history — show run history for a task
// --------------------------------------------------------------------------

func makeCmdHistory(cronStore store.CronStore) CommandHandler {
	return func(cc CommandContext) error {
		args := strings.Fields(cc.Args)
		if len(args) == 0 {
			cc.Reply("Usage: /history <task-id> [limit]")
			return nil
		}
		jobs, err := cronStore.ListJobs(cc.Ctx)
		if err != nil {
			return fmt.Errorf("failed to list jobs: %w", err)
		}
		job, err := resolveJobID(jobs, args[0])
		if err != nil {
			cc.Reply(err.Error())
			return nil
		}
		limit := 10
		if len(args) > 1 {
			if n, parseErr := strconv.Atoi(args[1]); parseErr == nil && n > 0 {
				limit = n
			}
		}
		results, err := cronStore.ListResults(cc.Ctx, job.ID, limit)
		if err != nil {
			return fmt.Errorf("failed to list results: %w", err)
		}
		if len(results) == 0 {
			cc.Reply(fmt.Sprintf("No runs found for task %s.", shortID(job.ID)))
			return nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("History for task %s (%d runs):\n\n", shortID(job.ID), len(results)))
		for _, r := range results {
			status := "ok"
			if r.ErrorMsg != "" {
				status = "error"
			}
			output := r.Output
			if len(output) > 100 {
				output = output[:97] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s  [%s]  %s\n", r.RanAt.Format("01-02 15:04"), status, output))
		}
		cc.Reply(sb.String())
		return nil
	}
}
