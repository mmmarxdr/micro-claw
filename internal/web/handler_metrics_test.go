package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"microagent/internal/audit"
	"microagent/internal/config"
	"microagent/internal/store"
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
			TotalTokens:   1000,
			EstimatedCost: 0.01,
		},
		history: []audit.DailyMetrics{
			{TotalTokens: 500, EstimatedCost: 0.005},
			{TotalTokens: 300, EstimatedCost: 0.003},
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

	if int(resp["tokens_today"].(float64)) != 1000 {
		t.Errorf("expected tokens_today=1000, got %v", resp["tokens_today"])
	}

	if resp["cost_today"].(float64) != 0.01 {
		t.Errorf("expected cost_today=0.01, got %v", resp["cost_today"])
	}

	if int(resp["tokens_month"].(float64)) != 800 {
		t.Errorf("expected tokens_month=800, got %v", resp["tokens_month"])
	}

	if resp["memory_count"].(float64) != 3 {
		t.Errorf("expected memory_count=3, got %v", resp["memory_count"])
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

	if resp["tokens_today"].(float64) != 0 {
		t.Errorf("expected tokens_today=0, got %v", resp["tokens_today"])
	}

	if resp["cost_today"].(float64) != 0 {
		t.Errorf("expected cost_today=0, got %v", resp["cost_today"])
	}
}

func TestHandleGetMetricsHistory_withAuditReader(t *testing.T) {
	aud := &fakeAuditReader{
		history: []audit.DailyMetrics{
			{Date: "2026-04-10", TotalTokens: 100},
			{Date: "2026-04-11", TotalTokens: 200},
		},
	}

	srv := newTestServerWithAuditor(t, &noWebStore{}, aud)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/history?days=7", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(items))
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

	var items []any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}

	if len(items) != 0 {
		t.Fatalf("expected empty array, got %d items", len(items))
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
