package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"daimon/internal/channel"
	"daimon/internal/content"
	cronpkg "daimon/internal/cron"
	"daimon/internal/provider"
	"daimon/internal/store"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockCronStore is an in-memory CronStore for testing.
type mockCronStore struct {
	jobs    map[string]store.CronJob
	results []store.CronResult

	// Optional error overrides.
	createErr error
	listErr   error
	deleteErr error
}

func newMockCronStore() *mockCronStore {
	return &mockCronStore{jobs: make(map[string]store.CronJob)}
}

func (m *mockCronStore) CreateJob(_ context.Context, job store.CronJob) (store.CronJob, error) {
	if m.createErr != nil {
		return store.CronJob{}, m.createErr
	}
	m.jobs[job.ID] = job
	return job, nil
}

func (m *mockCronStore) ListJobs(_ context.Context) ([]store.CronJob, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]store.CronJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out, nil
}

func (m *mockCronStore) GetJob(_ context.Context, id string) (store.CronJob, error) {
	j, ok := m.jobs[id]
	if !ok {
		return store.CronJob{}, store.ErrNotFound
	}
	return j, nil
}

func (m *mockCronStore) DeleteJob(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.jobs[id]; !ok {
		return store.ErrNotFound
	}
	delete(m.jobs, id)
	return nil
}

func (m *mockCronStore) SaveResult(_ context.Context, result store.CronResult) error {
	m.results = append(m.results, result)
	return nil
}

func (m *mockCronStore) ListResults(_ context.Context, jobID string, limit int) ([]store.CronResult, error) {
	var out []store.CronResult
	for _, r := range m.results {
		if r.JobID == jobID {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *mockCronStore) PruneResults(_ context.Context, _ int, _ int) error {
	return nil
}

func (m *mockCronStore) CountResults(_ context.Context, _ string) (int, error) { return 0, nil }

func (m *mockCronStore) UpdateJobRunTimes(_ context.Context, id string, lastRunAt, nextRunAt time.Time) error {
	j, ok := m.jobs[id]
	if !ok {
		return nil
	}
	j.LastRunAt = &lastRunAt
	j.NextRunAt = &nextRunAt
	m.jobs[id] = j
	return nil
}

// mockScheduler is a minimal scheduler for testing — no actual cron ticker.
type mockScheduler struct {
	jobs      map[string]store.CronJob
	removeErr error
}

func newMockScheduler() *mockScheduler {
	return &mockScheduler{jobs: make(map[string]store.CronJob)}
}

func (s *mockScheduler) AddJob(_ context.Context, job store.CronJob) error {
	s.jobs[job.ID] = job
	return nil
}

func (s *mockScheduler) RemoveJob(_ context.Context, id string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	if _, ok := s.jobs[id]; !ok {
		return errJobNotFoundMock
	}
	delete(s.jobs, id)
	return nil
}

// errJobNotFoundMock is a sentinel that satisfies the isNotFound check.
// We reuse cronpkg.ErrJobNotFound semantics by wrapping store.ErrNotFound.
var errJobNotFoundMock = store.ErrNotFound

// mockProvider returns a fixed response string or an error.
type mockProvider struct {
	response  string
	err       error
	callCount int
}

func (p *mockProvider) Name() string                                  { return "mock" }
func (p *mockProvider) Model() string                                 { return "mock-model" }
func (p *mockProvider) SupportsTools() bool                           { return false }
func (p *mockProvider) SupportsMultimodal() bool                      { return false }
func (p *mockProvider) SupportsAudio() bool                           { return false }
func (p *mockProvider) HealthCheck(_ context.Context) (string, error) { return "ok", nil }
func (p *mockProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	p.callCount++
	if p.err != nil {
		return nil, p.err
	}
	return &provider.ChatResponse{Content: p.response}, nil
}

// ---------------------------------------------------------------------------
// Cron tool builders for tests — using concrete struct types via thin wrappers
// ---------------------------------------------------------------------------

// buildScheduleTaskTool builds a scheduleTaskTool with mock deps, using a
// thin interface wrapper so the tests don't depend on the scheduler concrete type.
type testScheduleTaskTool struct {
	cronStore    *mockCronStore
	scheduler    *mockScheduler
	toolRegistry map[string]Tool
	prov         provider.Provider
	callSeq      int // incremented per call for unique IDs
}

func (t *testScheduleTaskTool) Name() string        { return "schedule_task" }
func (t *testScheduleTaskTool) Description() string { return "" }
func (t *testScheduleTaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{}`)
}

func (t *testScheduleTaskTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input scheduleTaskParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: "invalid parameters: " + err.Error()}, nil
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

	var cronExpr string
	if cronExprRegex.MatchString(input.Schedule) {
		cronExpr = strings.TrimSpace(input.Schedule)
	} else {
		// LLM conversion with retry.
		mp := t.prov.(*mockProvider)
		expr, err := callMockProviderForCron(ctx, t.prov, input.Schedule)
		if err == nil && cronExprRegex.MatchString(expr) {
			cronExpr = strings.TrimSpace(expr)
		} else {
			// Retry once.
			expr, err = callMockProviderForCron(ctx, t.prov, input.Schedule)
			if err != nil {
				return ToolResult{IsError: true, Content: "could not convert schedule to cron expression: " + err.Error()}, nil
			}
			if !cronExprRegex.MatchString(expr) {
				return ToolResult{IsError: true, Content: "could not parse schedule as cron expression (got: " + expr + "), provider called " + string(rune('0'+mp.callCount)) + " times"}, nil
			}
			cronExpr = strings.TrimSpace(expr)
		}
	}

	if err := validateCronExpr(cronExpr); err != nil {
		return ToolResult{IsError: true, Content: "invalid cron expression: " + err.Error()}, nil
	}

	nextRun, err := nextRunTime(cronExpr, time.Now())
	if err != nil {
		return ToolResult{IsError: true, Content: "could not compute next run time: " + err.Error()}, nil
	}

	// Keyword check.
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
			warnings = append(warnings, "Warning: this task mentions \""+keyword+"\" but no matching MCP tool is configured.")
		}
	}

	// Duplicate check.
	existing, err := t.cronStore.ListJobs(ctx)
	if err != nil {
		return ToolResult{IsError: true, Content: "could not check for duplicates: " + err.Error()}, nil
	}
	for _, j := range existing {
		if j.Prompt == input.Prompt && j.Schedule == cronExpr {
			warnings = append(warnings, "Warning: a job with the same prompt and schedule already exists (id: "+j.ID+"). Creating anyway.")
			break
		}
	}

	t.callSeq++
	human := humanReadable(cronExpr)
	job := store.CronJob{
		ID:            "test-id-" + strings.ReplaceAll(cronExpr, " ", "_") + "-" + string(rune('0'+t.callSeq)),
		Schedule:      cronExpr,
		ScheduleHuman: human,
		Prompt:        input.Prompt,
		ChannelID:     input.ChannelID,
		Enabled:       true,
		CreatedAt:     time.Now(),
		NextRunAt:     &nextRun,
	}

	created, err := t.cronStore.CreateJob(ctx, job)
	if err != nil {
		return ToolResult{IsError: true, Content: "could not save job: " + err.Error()}, nil
	}

	if err := t.scheduler.AddJob(ctx, created); err != nil {
		_ = t.cronStore.DeleteJob(ctx, created.ID)
		return ToolResult{IsError: true, Content: "could not register with scheduler: " + err.Error()}, nil
	}

	var sb strings.Builder
	sb.WriteString("Scheduled task (ID: " + created.ID + "):\n")
	sb.WriteString("- Prompt: \"" + input.Prompt + "\"\n")
	sb.WriteString("- Schedule: " + human + " (cron: `" + cronExpr + "`)\n")
	sb.WriteString("- Next run: " + nextRun.Format("Mon Jan 2 2006 at 15:04 MST") + "\n")
	sb.WriteString("- Results will be sent to: " + input.ChannelID + "\n")
	for _, w := range warnings {
		sb.WriteString("\n" + w + "\n")
	}

	return ToolResult{Content: sb.String()}, nil
}

func callMockProviderForCron(ctx context.Context, prov provider.Provider, schedule string) (string, error) {
	req := provider.ChatRequest{
		SystemPrompt: "You are a cron expression converter.",
		Messages:     []provider.ChatMessage{{Role: "user", Content: content.TextBlock(schedule)}},
		MaxTokens:    20,
	}
	resp, err := prov.Chat(ctx, req)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// buildDeleteCronTool builds a delete_cron tool backed by mock deps.
type testDeleteCronTool struct {
	cronStore *mockCronStore
	scheduler *mockScheduler
}

func (t *testDeleteCronTool) Name() string            { return "delete_cron" }
func (t *testDeleteCronTool) Description() string     { return "" }
func (t *testDeleteCronTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }

func (t *testDeleteCronTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input deleteCronParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: "invalid parameters: " + err.Error()}, nil
	}
	if strings.TrimSpace(input.ID) == "" {
		return ToolResult{IsError: true, Content: "id cannot be empty"}, nil
	}

	schedErr := t.scheduler.RemoveJob(ctx, input.ID)
	storeErr := t.cronStore.DeleteJob(ctx, input.ID)

	if isNotFound(schedErr) && isNotFound(storeErr) {
		return ToolResult{IsError: true, Content: "cron job \"" + input.ID + "\" not found"}, nil
	}
	if storeErr != nil && !isNotFound(storeErr) {
		return ToolResult{IsError: true, Content: "could not delete job: " + storeErr.Error()}, nil
	}

	return ToolResult{Content: "Deleted cron job " + input.ID + "."}, nil
}

// buildListCronsTool builds a list_crons tool backed by a mock store.
type testListCronsTool struct {
	cronStore *mockCronStore
}

func (t *testListCronsTool) Name() string            { return "list_crons" }
func (t *testListCronsTool) Description() string     { return "" }
func (t *testListCronsTool) Schema() json.RawMessage { return json.RawMessage(`{}`) }

func (t *testListCronsTool) Execute(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	lt := &listCronsTool{cronStore: t.cronStore}
	return lt.Execute(ctx, nil)
}

// ---------------------------------------------------------------------------
// TestScheduleTask_AlreadyCronExpression
// ---------------------------------------------------------------------------

func TestScheduleTask_AlreadyCronExpression(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{response: "should not be called"}
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"run daily report","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	// Provider should NOT have been called — schedule was already a cron expression.
	if prov.callCount != 0 {
		t.Errorf("expected 0 provider calls for cron expression input, got %d", prov.callCount)
	}
	// Job should be in the store.
	if len(cronStore.jobs) != 1 {
		t.Errorf("expected 1 job in store, got %d", len(cronStore.jobs))
	}
	// Output should mention the cron expression.
	if !strings.Contains(result.Content, "0 9 * * *") {
		t.Errorf("expected cron expression in output, got: %s", result.Content)
	}
	// Output should mention the human-readable schedule.
	if !strings.Contains(result.Content, "every day at 9:00 AM") {
		t.Errorf("expected human-readable schedule in output, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_NaturalLanguage_ProviderConverts
// ---------------------------------------------------------------------------

func TestScheduleTask_NaturalLanguage_ProviderConverts(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{response: "0 10 * * *"} // provider returns valid cron
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"every morning at 10am","prompt":"check news","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if prov.callCount < 1 {
		t.Errorf("expected provider to be called, got callCount=%d", prov.callCount)
	}
	if !strings.Contains(result.Content, "0 10 * * *") {
		t.Errorf("expected cron expr in output, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_MissingToolWarning
// ---------------------------------------------------------------------------

func TestScheduleTask_MissingToolWarning(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{response: "irrelevant"} // won't be called (cron expr input)
	// No tools in registry.
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	// Prompt mentions "email" — no email tool configured.
	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"send email report","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success (job should still be created), got error: %s", result.Content)
	}
	// Job should still be created.
	if len(cronStore.jobs) != 1 {
		t.Errorf("expected 1 job in store despite warning, got %d", len(cronStore.jobs))
	}
	// Output should contain a warning about email.
	if !strings.Contains(strings.ToLower(result.Content), "warning") {
		t.Errorf("expected warning in output, got: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "email") {
		t.Errorf("expected 'email' mentioned in warning, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_MissingToolWarning_ToolPresent
// ---------------------------------------------------------------------------

func TestScheduleTask_MissingToolWarning_ToolPresent(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{}
	// Add a gmail tool — should suppress email warning.
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{"gmail_send": &ShellTool{}},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"send email report","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	// Should NOT contain warning about email since the tool is present.
	if strings.Contains(strings.ToLower(result.Content), "warning") {
		t.Errorf("expected no warning when matching tool is present, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_InvalidSchedule_RetryFails
// ---------------------------------------------------------------------------

func TestScheduleTask_InvalidSchedule_RetryFails(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	// Provider always returns an invalid expression.
	prov := &mockProvider{response: "INVALID-CRON-RESPONSE"}
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	// Use a natural language schedule that won't match the 5-field cron regex.
	params := json.RawMessage(`{"schedule":"tomorrow morning sometime","prompt":"do something","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when schedule cannot be converted, got content: %s", result.Content)
	}
	// Should have retried (provider called twice).
	if prov.callCount < 2 {
		t.Errorf("expected at least 2 provider calls (1 + 1 retry), got %d", prov.callCount)
	}
	// No job should have been persisted.
	if len(cronStore.jobs) != 0 {
		t.Errorf("expected 0 jobs in store on failure, got %d", len(cronStore.jobs))
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_InvalidSchedule_ProviderError
// ---------------------------------------------------------------------------

func TestScheduleTask_InvalidSchedule_ProviderError(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{err: errors.New("LLM unavailable")}
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"every blue moon","prompt":"do something","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true on provider error, got: %s", result.Content)
	}
	if len(cronStore.jobs) != 0 {
		t.Errorf("expected 0 jobs on provider error, got %d", len(cronStore.jobs))
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTask_DuplicateWarning
// ---------------------------------------------------------------------------

func TestScheduleTask_DuplicateWarning(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()
	prov := &mockProvider{}
	tool := &testScheduleTaskTool{
		cronStore:    cronStore,
		scheduler:    scheduler,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	// Schedule the same job twice.
	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"daily report","channel_id":"cli"}`)
	_, _ = tool.Execute(context.Background(), params)

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// Second call should succeed but with a duplicate warning.
	if result.IsError {
		t.Errorf("expected success for duplicate schedule, got error: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "already exists") {
		t.Errorf("expected duplicate warning in output, got: %s", result.Content)
	}
	// Both jobs should be in the store.
	if len(cronStore.jobs) != 2 {
		t.Errorf("expected 2 jobs in store after duplicate schedule, got %d", len(cronStore.jobs))
	}
}

// ---------------------------------------------------------------------------
// TestListCrons_Empty
// ---------------------------------------------------------------------------

func TestListCrons_Empty(t *testing.T) {
	cronStore := newMockCronStore()
	tool := &testListCronsTool{cronStore: cronStore}

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success for empty list, got error: %s", result.Content)
	}
	if result.Content != "No scheduled tasks found." {
		t.Errorf("expected empty message, got: %q", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestListCrons_WithJobs
// ---------------------------------------------------------------------------

func TestListCrons_WithJobs(t *testing.T) {
	cronStore := newMockCronStore()

	nextA := time.Now().Add(time.Hour)
	nextB := time.Now().Add(2 * time.Hour)
	cronStore.jobs["job-aaa"] = store.CronJob{
		ID:            "job-aaa",
		Schedule:      "0 9 * * *",
		ScheduleHuman: "every day at 9:00 AM",
		Prompt:        "send daily report",
		ChannelID:     "cli",
		Enabled:       true,
		CreatedAt:     time.Now(),
		NextRunAt:     &nextA,
	}
	cronStore.jobs["job-bbb"] = store.CronJob{
		ID:            "job-bbb",
		Schedule:      "0 10 * * *",
		ScheduleHuman: "every day at 10:00 AM",
		Prompt:        "fetch weather",
		ChannelID:     "telegram:12345",
		Enabled:       true,
		CreatedAt:     time.Now(),
		NextRunAt:     &nextB,
	}

	tool := &testListCronsTool{cronStore: cronStore}
	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "job-aaa") {
		t.Errorf("expected job-aaa in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "job-bbb") {
		t.Errorf("expected job-bbb in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "0 9 * * *") {
		t.Errorf("expected schedule in output, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestListCrons_StoreError
// ---------------------------------------------------------------------------

func TestListCrons_StoreError(t *testing.T) {
	cronStore := newMockCronStore()
	cronStore.listErr = errors.New("db error")

	lt := &listCronsTool{cronStore: cronStore}
	result, err := lt.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true on store error, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteCron_Success
// ---------------------------------------------------------------------------

func TestDeleteCron_Success(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()

	// Pre-populate.
	cronStore.jobs["del-id-1"] = store.CronJob{ID: "del-id-1"}
	scheduler.jobs["del-id-1"] = store.CronJob{ID: "del-id-1"}

	tool := &testDeleteCronTool{cronStore: cronStore, scheduler: scheduler}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"del-id-1"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "del-id-1") {
		t.Errorf("expected job ID in success message, got: %s", result.Content)
	}
	if _, ok := cronStore.jobs["del-id-1"]; ok {
		t.Error("job should have been removed from store")
	}
	if _, ok := scheduler.jobs["del-id-1"]; ok {
		t.Error("job should have been removed from scheduler")
	}
}

// ---------------------------------------------------------------------------
// TestDeleteCron_NotFound
// ---------------------------------------------------------------------------

func TestDeleteCron_NotFound(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()

	tool := &testDeleteCronTool{cronStore: cronStore, scheduler: scheduler}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"nonexistent-id"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for not-found job, got: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "not found") {
		t.Errorf("expected 'not found' in error message, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteCron_EmptyID
// ---------------------------------------------------------------------------

func TestDeleteCron_EmptyID(t *testing.T) {
	cronStore := newMockCronStore()
	scheduler := newMockScheduler()

	tool := &testDeleteCronTool{cronStore: cronStore, scheduler: scheduler}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":""}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for empty ID")
	}
}

// ---------------------------------------------------------------------------
// TestHumanReadable
// ---------------------------------------------------------------------------

func TestHumanReadable(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"0 9 * * *", "every day at 9:00 AM"},
		{"0 * * * *", "every hour"},
		{"0 0 * * *", "every day at midnight"},
		{"0 12 * * *", "every day at noon"},
		{"0 9 * * 1-5", "every weekday at 9:00 AM"},
		{"5 3 * * 2", "cron: 5 3 * * 2"}, // fallback
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			got := humanReadable(tc.expr)
			if got != tc.want {
				t.Errorf("humanReadable(%q) = %q, want %q", tc.expr, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestNextRunTime
// ---------------------------------------------------------------------------

func TestNextRunTime(t *testing.T) {
	t.Run("valid expression returns future time", func(t *testing.T) {
		from := time.Date(2026, 3, 20, 8, 0, 0, 0, time.UTC)
		next, err := nextRunTime("0 9 * * *", from)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !next.After(from) {
			t.Errorf("next run %v is not after from %v", next, from)
		}
		if next.Hour() != 9 {
			t.Errorf("expected next run at hour 9, got %d", next.Hour())
		}
	})

	t.Run("invalid expression returns error", func(t *testing.T) {
		_, err := nextRunTime("not valid", time.Now())
		if err == nil {
			t.Error("expected error for invalid expression")
		}
	})
}

// ---------------------------------------------------------------------------
// TestBuildCronTools_Returns3Tools
// ---------------------------------------------------------------------------

func TestBuildCronTools_Returns3Tools(t *testing.T) {
	// This test verifies BuildCronTools returns exactly 3 tools.
	// It uses the real BuildCronTools but with nil scheduler/store/provider
	// and just checks the map keys — it doesn't call Execute.
	// We can't easily use nil scheduler here since it's *cronpkg.Scheduler.
	// Instead, verify the tool names via the concrete structs.
	st := &scheduleTaskTool{}
	lct := &listCronsTool{}
	dct := &deleteCronTool{}

	names := []string{st.Name(), lct.Name(), dct.Name()}
	expected := map[string]bool{
		"schedule_task": true,
		"list_crons":    true,
		"delete_cron":   true,
	}
	for _, name := range names {
		if !expected[name] {
			t.Errorf("unexpected tool name: %q", name)
		}
		delete(expected, name)
	}
	if len(expected) != 0 {
		t.Errorf("missing tool names: %v", expected)
	}
}

// ---------------------------------------------------------------------------
// TestBuildCronTools_ReturnsAllThree — tests the real BuildCronTools func
// ---------------------------------------------------------------------------

func TestBuildCronTools_ReturnsAllThree(t *testing.T) {
	cronStore := newMockCronStore()
	sched := &schedulerIfaceAdapter{newMockScheduler()}
	prov := &mockProvider{}

	tools := BuildCronTools(sched, cronStore, map[string]Tool{}, prov)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	for _, name := range []string{"schedule_task", "list_crons", "delete_cron"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("expected tool %q in map", name)
		}
	}
}

// schedulerIfaceAdapter wraps mockScheduler to satisfy cronpkg.SchedulerIface.
type schedulerIfaceAdapter struct {
	m *mockScheduler
}

func (a *schedulerIfaceAdapter) Start(_ context.Context, _ chan<- channel.IncomingMessage) error {
	return nil
}
func (a *schedulerIfaceAdapter) Stop() {}
func (a *schedulerIfaceAdapter) AddJob(ctx context.Context, job store.CronJob) error {
	return a.m.AddJob(ctx, job)
}

func (a *schedulerIfaceAdapter) RemoveJob(ctx context.Context, id string) error {
	return a.m.RemoveJob(ctx, id)
}

func (a *schedulerIfaceAdapter) ListActiveJobs(_ context.Context) ([]cronpkg.ActiveJob, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// TestScheduleTaskTool_Execute_AlreadyCronExpression
// Tests the REAL scheduleTaskTool.Execute (not the test wrapper)
// ---------------------------------------------------------------------------

func TestScheduleTaskTool_Execute_AlreadyCronExpression(t *testing.T) {
	cronStore := newMockCronStore()
	sched := &schedulerIfaceAdapter{newMockScheduler()}
	prov := &mockProvider{response: "should not be called"}

	tool := &scheduleTaskTool{
		scheduler:    sched,
		cronStore:    cronStore,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"check email","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	// Provider should NOT have been called — schedule was already a cron expression.
	if prov.callCount != 0 {
		t.Errorf("expected 0 provider calls for cron expression input, got %d", prov.callCount)
	}
	// Job should be in the store.
	if len(cronStore.jobs) != 1 {
		t.Errorf("expected 1 job in store, got %d", len(cronStore.jobs))
	}
	if !strings.Contains(result.Content, "0 9 * * *") {
		t.Errorf("expected cron expression in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "every day at 9:00 AM") {
		t.Errorf("expected human-readable schedule in output, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTaskTool_Execute_MissingToolWarning
// ---------------------------------------------------------------------------

func TestScheduleTaskTool_Execute_MissingToolWarning(t *testing.T) {
	cronStore := newMockCronStore()
	sched := &schedulerIfaceAdapter{newMockScheduler()}
	prov := &mockProvider{response: "irrelevant"} // won't be called (cron expr input)

	tool := &scheduleTaskTool{
		scheduler:    sched,
		cronStore:    cronStore,
		toolRegistry: map[string]Tool{}, // empty — no email tool
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"0 9 * * *","prompt":"send email report","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success (job should still be created), got error: %s", result.Content)
	}
	if len(cronStore.jobs) != 1 {
		t.Errorf("expected 1 job in store despite warning, got %d", len(cronStore.jobs))
	}
	if !strings.Contains(strings.ToLower(result.Content), "warning") {
		t.Errorf("expected warning in output, got: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "email") {
		t.Errorf("expected 'email' mentioned in warning, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestScheduleTaskTool_Execute_InvalidSchedule
// Provider returns non-cron twice → IsError:true
// ---------------------------------------------------------------------------

func TestScheduleTaskTool_Execute_InvalidSchedule(t *testing.T) {
	cronStore := newMockCronStore()
	sched := &schedulerIfaceAdapter{newMockScheduler()}
	prov := &mockProvider{response: "not a cron"}

	tool := &scheduleTaskTool{
		scheduler:    sched,
		cronStore:    cronStore,
		toolRegistry: map[string]Tool{},
		prov:         prov,
	}

	params := json.RawMessage(`{"schedule":"tomorrow morning sometime","prompt":"do something","channel_id":"cli"}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true when schedule cannot be converted, got content: %s", result.Content)
	}
	if prov.callCount < 2 {
		t.Errorf("expected at least 2 provider calls (1 + 1 retry), got %d", prov.callCount)
	}
	if len(cronStore.jobs) != 0 {
		t.Errorf("expected 0 jobs in store on failure, got %d", len(cronStore.jobs))
	}
}

// ---------------------------------------------------------------------------
// TestDeleteCronTool_Execute_Found — uses real deleteCronTool
// ---------------------------------------------------------------------------

func TestDeleteCronTool_Execute_Found(t *testing.T) {
	cronStore := newMockCronStore()
	mockSched := newMockScheduler()
	sched := &schedulerIfaceAdapter{mockSched}

	// Pre-register job in both store and mock scheduler.
	cronStore.jobs["real-del-id"] = store.CronJob{ID: "real-del-id"}
	mockSched.jobs["real-del-id"] = store.CronJob{ID: "real-del-id"}

	tool := &deleteCronTool{scheduler: sched, cronStore: cronStore}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"real-del-id"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success, got error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "real-del-id") {
		t.Errorf("expected job ID in success message, got: %s", result.Content)
	}
	if _, ok := cronStore.jobs["real-del-id"]; ok {
		t.Error("job should have been removed from store")
	}
	if _, ok := mockSched.jobs["real-del-id"]; ok {
		t.Error("job should have been removed from scheduler")
	}
}

// ---------------------------------------------------------------------------
// TestDeleteCronTool_Execute_NotFound — uses real deleteCronTool
// ---------------------------------------------------------------------------

func TestDeleteCronTool_Execute_NotFound(t *testing.T) {
	cronStore := newMockCronStore()
	sched := &schedulerIfaceAdapter{newMockScheduler()}

	tool := &deleteCronTool{scheduler: sched, cronStore: cronStore}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"unknown-id"}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for not-found job, got: %s", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), "not found") {
		t.Errorf("expected 'not found' in error message, got: %s", result.Content)
	}
}

// ---------------------------------------------------------------------------
// TestCronExprRegex
// ---------------------------------------------------------------------------

func TestCronExprRegex(t *testing.T) {
	valid := []string{
		"0 9 * * *",
		"0 10 * * *",
		"*/5 * * * *",
		"0 9 * * 1-5",
		"0 0 1 * *",
	}
	invalid := []string{
		"every morning at 9am",
		"9am daily",
		"",
		"0 9 * *",     // only 4 fields
		"0 9 * * * *", // 6 fields (seconds — not standard 5-field)
	}

	for _, expr := range valid {
		if !cronExprRegex.MatchString(expr) {
			t.Errorf("expected %q to match cron expr regex", expr)
		}
	}
	for _, expr := range invalid {
		if cronExprRegex.MatchString(expr) {
			t.Errorf("expected %q to NOT match cron expr regex", expr)
		}
	}
}
