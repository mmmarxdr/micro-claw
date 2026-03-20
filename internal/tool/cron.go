package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	robfigcron "github.com/robfig/cron/v3"

	cronpkg "microagent/internal/cron"
	"microagent/internal/provider"
	"microagent/internal/store"
)

var cronExprRegex = regexp.MustCompile(`^\s*(\S+\s+){4}\S+\s*$`)

// cronHumanMap maps common 5-field cron expressions to human-readable descriptions.
var cronHumanMap = map[string]string{
	"0 * * * *":   "every hour",
	"0 9 * * *":   "every day at 9:00 AM",
	"0 10 * * *":  "every day at 10:00 AM",
	"0 8 * * 1":   "every Monday at 8:00 AM",
	"0 9 * * 1-5": "every weekday at 9:00 AM",
	"0 0 * * *":   "every day at midnight",
	"0 12 * * *":  "every day at noon",
	"0 0 * * 1":   "every Monday at midnight",
	"0 0 1 * *":   "first of every month",
}

// keywordToolHints maps prompt keywords to lists of tool name hints.
var keywordToolHints = map[string][]string{
	"email":    {"gmail", "email", "mail", "imap"},
	"mail":     {"gmail", "email", "mail", "imap"},
	"calendar": {"calendar", "gcal"},
	"slack":    {"slack"},
	"github":   {"github", "git"},
	"notion":   {"notion"},
	"weather":  {"weather", "openweathermap"},
	"browser":  {"browser", "puppeteer", "playwright"},
}

// humanReadable converts a 5-field cron expression to a human-readable description.
func humanReadable(expr string) string {
	trimmed := strings.TrimSpace(expr)
	if h, ok := cronHumanMap[trimmed]; ok {
		return h
	}
	return "cron: " + trimmed
}

// validateCronExpr validates a cron expression using the robfig parser.
func validateCronExpr(expr string) error {
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	_, err := parser.Parse(strings.TrimSpace(expr))
	return err
}

// nextRunTime computes the next run time for a cron expression.
func nextRunTime(expr string, from time.Time) (time.Time, error) {
	parser := robfigcron.NewParser(robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	sched, err := parser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

// BuildCronTools constructs the three cron built-in tools and returns them
// as a map ready for MergeTools. Called from BuildRegistry when cron is enabled.
func BuildCronTools(
	scheduler cronpkg.SchedulerIface,
	cronStore store.CronStore,
	toolRegistry map[string]Tool,
	prov provider.Provider,
) map[string]Tool {
	m := make(map[string]Tool)

	st := &scheduleTaskTool{
		scheduler:    scheduler,
		cronStore:    cronStore,
		toolRegistry: toolRegistry,
		prov:         prov,
	}
	m[st.Name()] = st

	lct := &listCronsTool{cronStore: cronStore}
	m[lct.Name()] = lct

	dct := &deleteCronTool{
		scheduler: scheduler,
		cronStore: cronStore,
	}
	m[dct.Name()] = dct

	return m
}

// ---------------------------------------------------------------------------
// schedule_task tool
// ---------------------------------------------------------------------------

type scheduleTaskTool struct {
	scheduler    cronpkg.SchedulerIface
	cronStore    store.CronStore
	toolRegistry map[string]Tool
	prov         provider.Provider
}

func (t *scheduleTaskTool) Name() string { return "schedule_task" }

func (t *scheduleTaskTool) Description() string {
	return "Schedule a recurring task. Provide a natural language schedule (e.g. 'every morning at 10am') or a 5-field cron expression. The task prompt will be executed at the scheduled time and results sent to the specified channel."
}

func (t *scheduleTaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["schedule", "prompt", "channel_id"],
  "properties": {
    "schedule": {
      "type": "string",
      "description": "Natural language schedule or cron expression (e.g. 'every morning at 10am', '0 10 * * *')"
    },
    "prompt": {
      "type": "string",
      "description": "The task the agent should perform at the scheduled time"
    },
    "channel_id": {
      "type": "string",
      "description": "Channel ID to send results back to (e.g. 'cli', 'telegram:123456')"
    }
  }
}`)
}

type scheduleTaskParams struct {
	Schedule  string `json:"schedule"`
	Prompt    string `json:"prompt"`
	ChannelID string `json:"channel_id"`
}

func (t *scheduleTaskTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input scheduleTaskParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.Schedule) == "" {
		return ToolResult{IsError: true, Content: "schedule cannot be empty"}, nil
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return ToolResult{IsError: true, Content: "prompt cannot be empty"}, nil
	}
	if strings.TrimSpace(input.ChannelID) == "" {
		return ToolResult{IsError: true, Content: "channel_id cannot be empty"}, nil
	}

	// Step 1: Detect if already a cron expression.
	var cronExpr string
	if cronExprRegex.MatchString(input.Schedule) {
		cronExpr = strings.TrimSpace(input.Schedule)
	} else {
		// Step 2: LLM sub-call to convert natural language to cron expression.
		var err error
		cronExpr, err = t.convertScheduleToCron(ctx, input.Schedule)
		if err != nil {
			return ToolResult{IsError: true, Content: err.Error()}, nil
		}
	}

	// Step 3: Validate the cron expression.
	if err := validateCronExpr(cronExpr); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid cron expression %q: %v", cronExpr, err)}, nil
	}

	// Step 4: Compute next run time.
	nextRun, err := nextRunTime(cronExpr, time.Now())
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not compute next run time: %v", err)}, nil
	}

	// Step 5: Keyword check against toolRegistry — collect warnings.
	var warnings []string
	promptLower := strings.ToLower(input.Prompt)
	for keyword, hints := range keywordToolHints {
		if !strings.Contains(promptLower, keyword) {
			continue
		}
		found := false
		for toolName := range t.toolRegistry {
			toolNameLower := strings.ToLower(toolName)
			for _, hint := range hints {
				if strings.Contains(toolNameLower, hint) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: this task mentions %q but no matching MCP tool is configured. Run `microagent mcp add ...` to add one.",
				keyword,
			))
		}
	}

	// Step 6: Duplicate check.
	existingJobs, err := t.cronStore.ListJobs(ctx)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not check for duplicate jobs: %v", err)}, nil
	}
	for _, j := range existingJobs {
		if j.Prompt == input.Prompt && j.Schedule == cronExpr {
			warnings = append(warnings, fmt.Sprintf(
				"Warning: a job with the same prompt and schedule already exists (id: %s). Creating anyway.",
				j.ID,
			))
			break
		}
	}

	// Step 7: Persist the job.
	human := humanReadable(cronExpr)
	jobID := uuid.New().String()
	job := store.CronJob{
		ID:            jobID,
		Schedule:      cronExpr,
		ScheduleHuman: human,
		Prompt:        input.Prompt,
		ChannelID:     input.ChannelID,
		Enabled:       true,
		CreatedAt:     time.Now(),
		NextRunAt:     &nextRun,
	}

	createdJob, err := t.cronStore.CreateJob(ctx, job)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not save job: %v", err)}, nil
	}

	// Step 8: Register with scheduler.
	if err := t.scheduler.AddJob(ctx, createdJob); err != nil {
		// Attempt to roll back by deleting from store (best effort).
		_ = t.cronStore.DeleteJob(ctx, createdJob.ID)
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not register job with scheduler: %v", err)}, nil
	}

	// Step 9: Build response text.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled task (ID: %s):\n", createdJob.ID)
	fmt.Fprintf(&sb, "- Prompt: %q\n", input.Prompt)
	fmt.Fprintf(&sb, "- Schedule: %s (cron: `%s`)\n", human, cronExpr)
	fmt.Fprintf(&sb, "- Next run: %s\n", nextRun.Format("Mon Jan 2 2006 at 15:04 MST"))
	fmt.Fprintf(&sb, "- Results will be sent to: %s\n", input.ChannelID)
	for _, w := range warnings {
		fmt.Fprintf(&sb, "\n%s\n", w)
	}

	return ToolResult{Content: sb.String()}, nil
}

// convertScheduleToCron uses the provider to convert a natural language schedule
// string to a 5-field cron expression. Retries once on failure.
func (t *scheduleTaskTool) convertScheduleToCron(ctx context.Context, schedule string) (string, error) {
	expr, err := t.callProviderForCron(ctx, schedule)
	if err == nil && cronExprRegex.MatchString(expr) {
		return strings.TrimSpace(expr), nil
	}

	// Retry once.
	expr, err = t.callProviderForCron(ctx, schedule)
	if err != nil {
		return "", fmt.Errorf("could not convert schedule to cron expression: %v", err)
	}
	if !cronExprRegex.MatchString(expr) {
		return "", fmt.Errorf("could not parse schedule %q as a cron expression (got: %q)", schedule, expr)
	}
	return strings.TrimSpace(expr), nil
}

func (t *scheduleTaskTool) callProviderForCron(ctx context.Context, schedule string) (string, error) {
	req := provider.ChatRequest{
		SystemPrompt: "You are a cron expression converter. Reply with ONLY a standard 5-field cron expression (minute hour day-of-month month day-of-week). No explanation, no markdown, no quotes.",
		Messages: []provider.ChatMessage{
			{
				Role:    "user",
				Content: fmt.Sprintf("Convert this schedule to a cron expression: %s", schedule),
			},
		},
		MaxTokens: 20,
	}
	resp, err := t.prov.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// ---------------------------------------------------------------------------
// list_crons tool
// ---------------------------------------------------------------------------

type listCronsTool struct {
	cronStore store.CronStore
}

func (t *listCronsTool) Name() string { return "list_crons" }

func (t *listCronsTool) Description() string {
	return "List all scheduled recurring tasks. Returns a table with job IDs, schedules, next run times, and prompts."
}

func (t *listCronsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

func (t *listCronsTool) Execute(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	jobs, err := t.cronStore.ListJobs(ctx)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not list jobs: %v", err)}, nil
	}

	if len(jobs) == 0 {
		return ToolResult{Content: "No scheduled tasks found."}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-16s  %-18s  %-25s  %s\n", "ID", "SCHEDULE", "NEXT RUN", "PROMPT")
	fmt.Fprintf(&sb, "%-16s  %-18s  %-25s  %s\n",
		strings.Repeat("-", 16),
		strings.Repeat("-", 18),
		strings.Repeat("-", 25),
		strings.Repeat("-", 30),
	)

	for _, job := range jobs {
		nextRun := "—"
		if job.NextRunAt != nil {
			nextRun = job.NextRunAt.Format(time.RFC1123)
		}
		prompt := job.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		fmt.Fprintf(&sb, "%-16s  %-18s  %-25s  %s\n",
			job.ID,
			job.Schedule,
			nextRun,
			prompt,
		)
	}

	return ToolResult{Content: sb.String()}, nil
}

// ---------------------------------------------------------------------------
// delete_cron tool
// ---------------------------------------------------------------------------

type deleteCronTool struct {
	scheduler cronpkg.SchedulerIface
	cronStore store.CronStore
}

func (t *deleteCronTool) Name() string { return "delete_cron" }

func (t *deleteCronTool) Description() string {
	return "Delete a scheduled recurring task by its job ID. The job will be immediately removed and will not fire again."
}

func (t *deleteCronTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["id"],
  "properties": {
    "id": {
      "type": "string",
      "description": "The job ID to delete"
    }
  }
}`)
}

type deleteCronParams struct {
	ID string `json:"id"`
}

func (t *deleteCronTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input deleteCronParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.ID) == "" {
		return ToolResult{IsError: true, Content: "id cannot be empty"}, nil
	}

	// Remove from scheduler (may return ErrJobNotFound if not currently loaded).
	schedErr := t.scheduler.RemoveJob(ctx, input.ID)

	// Delete from store.
	storeErr := t.cronStore.DeleteJob(ctx, input.ID)

	// If both returned not-found errors, report not found.
	if isNotFound(schedErr) && isNotFound(storeErr) {
		return ToolResult{IsError: true, Content: fmt.Sprintf("cron job %q not found", input.ID)}, nil
	}

	// If the store delete failed with a real error, report it.
	if storeErr != nil && !isNotFound(storeErr) {
		return ToolResult{IsError: true, Content: fmt.Sprintf("could not delete job from store: %v", storeErr)}, nil
	}

	return ToolResult{Content: fmt.Sprintf("Deleted cron job %s.", input.ID)}, nil
}

// isNotFound returns true if the error indicates a job was not found.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, store.ErrNotFound) || errors.Is(err, cronpkg.ErrJobNotFound)
}
