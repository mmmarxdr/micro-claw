package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daimon/internal/audit"
	"daimon/internal/config"
	"daimon/internal/store"
)

// fakeAuditReader implements both audit.Auditor and audit.AuditReader.
type fakeAuditReader struct {
	today   audit.DailyMetrics
	history []audit.DailyMetrics
}

func (f *fakeAuditReader) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (f *fakeAuditReader) Close() error                                     { return nil }
func (f *fakeAuditReader) TodayMetrics(_ context.Context) (audit.DailyMetrics, error) {
	return f.today, nil
}

func (f *fakeAuditReader) MetricsHistory(_ context.Context, _ int) ([]audit.DailyMetrics, error) {
	return f.history, nil
}

// noAuditReader implements audit.Auditor only (no AuditReader).
type noAuditReader struct{}

func (noAuditReader) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (noAuditReader) Close() error                                     { return nil }

func newTestServerWithAuditor(t *testing.T, st store.Store, aud audit.Auditor) *Server {
	t.Helper()

	s := &Server{
		deps: ServerDeps{
			Store:     st,
			Auditor:   aud,
			StartedAt: time.Now(),
			Config:    &config.Config{},
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	return s
}

func TestHandleGetMetrics_withAuditReader(t *testing.T) {
	aud := &fakeAuditReader{
		today: audit.DailyMetrics{
			Date:          "2026-04-11",
			InputTokens:   700,
			OutputTokens:  300,
			TotalTokens:   1000,
			EstimatedCost: 0.01,
		},
		history: []audit.DailyMetrics{
			{InputTokens: 300, OutputTokens: 200, TotalTokens: 500, EstimatedCost: 0.005},
			{InputTokens: 200, OutputTokens: 100, TotalTokens: 300, EstimatedCost: 0.003},
		},
	}

	st := &fakeWebStore{
		memory: []store.MemoryEntry{{ID: "1"}, {ID: "2"}, {ID: "3"}},
	}

	srv := newTestServerWithAuditor(t, st, aud)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	today, ok := resp["today"].(map[string]any)
	if !ok {
		t.Fatalf("expected today object, got %T: %v", resp["today"], resp["today"])
	}

	if today["input_tokens"].(float64) != 700 {
		t.Errorf("expected today.input_tokens=700, got %v", today["input_tokens"])
	}
	if today["cost_usd"].(float64) != 0.01 {
		t.Errorf("expected today.cost_usd=0.01, got %v", today["cost_usd"])
	}

	month, ok := resp["month"].(map[string]any)
	if !ok {
		t.Fatalf("expected month object, got %T", resp["month"])
	}
	// month aggregates from history: 300+200=500 input, 200+100=300 output
	if month["input_tokens"].(float64) != 500 {
		t.Errorf("expected month.input_tokens=500, got %v", month["input_tokens"])
	}
	if month["cost_usd"].(float64) != 0.008 {
		t.Errorf("expected month.cost_usd=0.008, got %v", month["cost_usd"])
	}

	history, ok := resp["history"].([]any)
	if !ok {
		t.Fatalf("expected history array, got %T", resp["history"])
	}
	if len(history) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(history))
	}
}

func TestHandleGetMetrics_noAuditReader(t *testing.T) {
	srv := newTestServerWithAuditor(t, &noWebStore{}, noAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	today, ok := resp["today"].(map[string]any)
	if !ok {
		t.Fatalf("expected today object, got %T", resp["today"])
	}
	if today["cost_usd"].(float64) != 0 {
		t.Errorf("expected today.cost_usd=0, got %v", today["cost_usd"])
	}
	if today["input_tokens"].(float64) != 0 {
		t.Errorf("expected today.input_tokens=0, got %v", today["input_tokens"])
	}
}

func TestHandleGetMetricsHistory_withAuditReader(t *testing.T) {
	aud := &fakeAuditReader{
		history: []audit.DailyMetrics{
			{Date: "2026-04-10", InputTokens: 60, OutputTokens: 40, TotalTokens: 100},
			{Date: "2026-04-11", InputTokens: 120, OutputTokens: 80, TotalTokens: 200},
		},
	}

	srv := newTestServerWithAuditor(t, &noWebStore{}, aud)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/history?days=7", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var snap map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}

	history, ok := snap["history"].([]any)
	if !ok {
		t.Fatalf("expected history array, got %T", snap["history"])
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(history))
	}
}

func TestHandleGetMetricsHistory_noAuditReader(t *testing.T) {
	srv := newTestServerWithAuditor(t, &noWebStore{}, noAuditReader{})

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/history", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var snap map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}

	history, ok := snap["history"].([]any)
	if !ok {
		t.Fatalf("expected history array, got %T", snap["history"])
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history array, got %d items", len(history))
	}
}

func TestHandleGetMetricsHistory_daysClamped(t *testing.T) {
	aud := &fakeAuditReader{}
	srv := newTestServerWithAuditor(t, &noWebStore{}, aud)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/history?days=999", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	// Should not panic and return 200
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
