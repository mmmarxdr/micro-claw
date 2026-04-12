package audit

import (
	"context"
	"testing"
	"time"
)

// helpers

func newTestAuditor(t *testing.T) *SQLiteAuditor {
	t.Helper()
	a, err := NewSQLiteAuditor(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteAuditor: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func emitLLMCall(t *testing.T, a *SQLiteAuditor, id string, model string, input, output int, ts time.Time) {
	t.Helper()
	err := a.Emit(context.Background(), AuditEvent{
		ID:           id,
		ScopeID:      "test",
		EventType:    "llm_call",
		Timestamp:    ts,
		Model:        model,
		InputTokens:  input,
		OutputTokens: output,
	})
	if err != nil {
		t.Fatalf("Emit %q: %v", id, err)
	}
}

// TestAuditReader_TypeAssertion verifies that SQLiteAuditor satisfies AuditReader
// and that NoopAuditor does not.
func TestAuditReader_TypeAssertion(t *testing.T) {
	a := newTestAuditor(t)
	if _, ok := any(a).(AuditReader); !ok {
		t.Error("SQLiteAuditor should implement AuditReader")
	}

	var noop Auditor = NoopAuditor{}
	if _, ok := noop.(AuditReader); ok {
		t.Error("NoopAuditor must NOT implement AuditReader")
	}
}

// TestTodayMetrics_NoEvents verifies that TodayMetrics returns all-zero values
// when there are no events recorded for today.
func TestTodayMetrics_NoEvents(t *testing.T) {
	a := newTestAuditor(t)

	m, err := a.TodayMetrics(context.Background())
	if err != nil {
		t.Fatalf("TodayMetrics: %v", err)
	}

	if m.InputTokens != 0 {
		t.Errorf("InputTokens: want 0, got %d", m.InputTokens)
	}
	if m.OutputTokens != 0 {
		t.Errorf("OutputTokens: want 0, got %d", m.OutputTokens)
	}
	if m.TotalTokens != 0 {
		t.Errorf("TotalTokens: want 0, got %d", m.TotalTokens)
	}
	if m.RequestCount != 0 {
		t.Errorf("RequestCount: want 0, got %d", m.RequestCount)
	}
	if m.EstimatedCost != 0 {
		t.Errorf("EstimatedCost: want 0, got %f", m.EstimatedCost)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if m.Date != today {
		t.Errorf("Date: want %q, got %q", today, m.Date)
	}
}

// TestTodayMetrics_ThreeEvents verifies that TodayMetrics correctly sums tokens
// and counts requests across three LLM call events.
func TestTodayMetrics_ThreeEvents(t *testing.T) {
	a := newTestAuditor(t)
	now := time.Now().UTC()

	emitLLMCall(t, a, "t1", "gpt-4o-mini", 100, 50, now)
	emitLLMCall(t, a, "t2", "gpt-4o-mini", 200, 80, now)
	emitLLMCall(t, a, "t3", "gpt-4o-mini", 300, 120, now)

	// Also emit a tool_use event — must NOT be counted.
	_ = a.Emit(context.Background(), AuditEvent{
		ID:        "tool1",
		ScopeID:   "test",
		EventType: "tool_use",
		Timestamp: now,
		ToolName:  "shell_exec",
	})

	m, err := a.TodayMetrics(context.Background())
	if err != nil {
		t.Fatalf("TodayMetrics: %v", err)
	}

	if m.InputTokens != 600 {
		t.Errorf("InputTokens: want 600, got %d", m.InputTokens)
	}
	if m.OutputTokens != 250 {
		t.Errorf("OutputTokens: want 250, got %d", m.OutputTokens)
	}
	if m.TotalTokens != 850 {
		t.Errorf("TotalTokens: want 850, got %d", m.TotalTokens)
	}
	if m.RequestCount != 3 {
		t.Errorf("RequestCount: want 3, got %d", m.RequestCount)
	}

	wantCost := EstimateCost("gpt-4o-mini", 600, 250)
	if m.EstimatedCost != wantCost {
		t.Errorf("EstimatedCost: want %f, got %f", wantCost, m.EstimatedCost)
	}
}

// TestMetricsHistory_SevenDaysThreeActive verifies that MetricsHistory returns
// exactly [days] entries, with zero-filled entries for days that have no events.
func TestMetricsHistory_SevenDaysThreeActive(t *testing.T) {
	a := newTestAuditor(t)
	now := time.Now().UTC()

	// Emit events on 3 distinct days within the 7-day window.
	day0 := now // today
	day2 := now.AddDate(0, 0, -2)
	day5 := now.AddDate(0, 0, -5)

	emitLLMCall(t, a, "h1", "gpt-4o", 1000, 500, day0)
	emitLLMCall(t, a, "h2", "gpt-4o", 2000, 800, day2)
	emitLLMCall(t, a, "h3", "gpt-4o", 3000, 1200, day5)
	// Emit one event 8 days ago — must NOT appear in a 7-day window.
	emitLLMCall(t, a, "h4", "gpt-4o", 9999, 9999, now.AddDate(0, 0, -8))

	history, err := a.MetricsHistory(context.Background(), 7)
	if err != nil {
		t.Fatalf("MetricsHistory: %v", err)
	}

	if len(history) != 7 {
		t.Fatalf("len(history): want 7, got %d", len(history))
	}

	// Dates must be in ascending order.
	for i := 1; i < len(history); i++ {
		if history[i].Date <= history[i-1].Date {
			t.Errorf("history not sorted: history[%d].Date=%q <= history[%d].Date=%q",
				i, history[i].Date, i-1, history[i-1].Date)
		}
	}

	// Verify specific active days have non-zero data.
	dayMap := make(map[string]DailyMetrics, 7)
	for _, m := range history {
		dayMap[m.Date] = m
	}

	activeDay := func(offset int) string {
		return now.AddDate(0, 0, offset).Format("2006-01-02")
	}

	checkActive := func(offset int, wantIn, wantOut int64) {
		t.Helper()
		d := activeDay(offset)
		m, ok := dayMap[d]
		if !ok {
			t.Errorf("day %q not in history", d)
			return
		}
		if m.InputTokens != wantIn {
			t.Errorf("day %q InputTokens: want %d, got %d", d, wantIn, m.InputTokens)
		}
		if m.OutputTokens != wantOut {
			t.Errorf("day %q OutputTokens: want %d, got %d", d, wantOut, m.OutputTokens)
		}
		if m.RequestCount != 1 {
			t.Errorf("day %q RequestCount: want 1, got %d", d, m.RequestCount)
		}
	}

	checkActive(0, 1000, 500)
	checkActive(-2, 2000, 800)
	checkActive(-5, 3000, 1200)

	// The remaining 4 days must be zero-filled.
	zeroCount := 0
	for _, m := range history {
		if m.RequestCount == 0 {
			zeroCount++
			if m.InputTokens != 0 || m.OutputTokens != 0 || m.TotalTokens != 0 || m.EstimatedCost != 0 {
				t.Errorf("zero day %q has non-zero fields: %+v", m.Date, m)
			}
		}
	}
	if zeroCount != 4 {
		t.Errorf("zero-filled days: want 4, got %d", zeroCount)
	}
}

// TestMetricsHistory_ZeroDays verifies that MetricsHistory(0) returns nil.
func TestMetricsHistory_ZeroDays(t *testing.T) {
	a := newTestAuditor(t)
	history, err := a.MetricsHistory(context.Background(), 0)
	if err != nil {
		t.Fatalf("MetricsHistory(0): %v", err)
	}
	if history != nil {
		t.Errorf("expected nil, got %v", history)
	}
}

// TestEstimateCost_KnownModel verifies correct cost calculation for a known model.
func TestEstimateCost_KnownModel(t *testing.T) {
	// gpt-4o: $2.50/1M input, $10.00/1M output
	cost := EstimateCost("gpt-4o", 1_000_000, 500_000)
	want := 2.50 + 5.00 // 1M * 2.50 + 0.5M * 10.0
	if cost != want {
		t.Errorf("EstimateCost gpt-4o: want %f, got %f", want, cost)
	}
}

// TestEstimateCost_UnknownModel verifies that unknown models return 0.
func TestEstimateCost_UnknownModel(t *testing.T) {
	for _, model := range []string{"unknown-model-xyz", "", "future-model-v99"} {
		cost := EstimateCost(model, 1_000_000, 1_000_000)
		if cost != 0 {
			t.Errorf("EstimateCost(%q): want 0, got %f", model, cost)
		}
	}
}

// TestEstimateCost_ZeroTokens verifies that zero tokens always produce zero cost.
func TestEstimateCost_ZeroTokens(t *testing.T) {
	for model := range modelPricing {
		cost := EstimateCost(model, 0, 0)
		if cost != 0 {
			t.Errorf("EstimateCost(%q, 0, 0): want 0, got %f", model, cost)
		}
	}
}

// TestEstimateCost_SmallFraction verifies sub-1M token costs are computed correctly.
func TestEstimateCost_SmallFraction(t *testing.T) {
	// gemini-2.0-flash: $0.075/1M input, $0.30/1M output
	cost := EstimateCost("gemini-2.0-flash", 100_000, 50_000)
	wantInput := 0.1 * 0.075
	wantOutput := 0.05 * 0.30
	want := wantInput + wantOutput
	// Use epsilon comparison for floating-point.
	const eps = 1e-9
	diff := cost - want
	if diff < -eps || diff > eps {
		t.Errorf("EstimateCost gemini-2.0-flash(100k,50k): want %f, got %f", want, cost)
	}
}

// TestMetricsHistory_DateLabels verifies that every returned entry has a well-formed
// YYYY-MM-DD date label and that dates span exactly [days] consecutive days.
func TestMetricsHistory_DateLabels(t *testing.T) {
	a := newTestAuditor(t)

	const days = 5
	history, err := a.MetricsHistory(context.Background(), days)
	if err != nil {
		t.Fatalf("MetricsHistory: %v", err)
	}
	if len(history) != days {
		t.Fatalf("len: want %d, got %d", days, len(history))
	}

	now := time.Now().UTC()
	for i, m := range history {
		want := now.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		if m.Date != want {
			t.Errorf("history[%d].Date: want %q, got %q", i, want, m.Date)
		}
	}
}

// TestTodayMetrics_MixedModels verifies cost is aggregated across multiple models.
func TestTodayMetrics_MixedModels(t *testing.T) {
	a := newTestAuditor(t)
	now := time.Now().UTC()

	emitLLMCall(t, a, "m1", "gpt-4o", 1_000_000, 0, now)
	emitLLMCall(t, a, "m2", "gpt-4o-mini", 0, 1_000_000, now)

	m, err := a.TodayMetrics(context.Background())
	if err != nil {
		t.Fatalf("TodayMetrics: %v", err)
	}

	// gpt-4o: 1M input = $2.50; gpt-4o-mini: 1M output = $0.60
	wantCost := 2.50 + 0.60
	const eps = 1e-9
	diff := m.EstimatedCost - wantCost
	if diff < -eps || diff > eps {
		t.Errorf("EstimatedCost: want %f, got %f", wantCost, m.EstimatedCost)
	}
	if m.RequestCount != 2 {
		t.Errorf("RequestCount: want 2, got %d", m.RequestCount)
	}
}
