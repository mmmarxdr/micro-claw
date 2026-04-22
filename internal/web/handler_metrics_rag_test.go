package web

// M11: GET /api/metrics/rag with populated RingRecorder returns 200 + valid JSON.
// M12: GET /api/metrics/rag when RAGMetrics == nil returns 501.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daimon/internal/rag/metrics"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newMetricsRAGTestServer(rec metrics.Recorder) *Server {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "test-token"
	deps := ServerDeps{
		Config:     cfg,
		RAGMetrics: rec,
	}
	return NewServer(deps)
}

// ---------------------------------------------------------------------------
// M11: GET /api/metrics/rag with populated recorder returns 200 + valid JSON,
//      events newest-first.
// ---------------------------------------------------------------------------

func TestHandleGetRAGMetrics_WithEvents(t *testing.T) {
	rec := metrics.NewRingRecorder(50)
	// Record 10 events with increasing TotalDurationMs so we can verify order.
	for i := 1; i <= 10; i++ {
		rec.Record(metrics.Event{
			Timestamp:           time.Now(),
			Query:               "query",
			TotalDurationMs:     int64(i * 10),
			FinalChunksReturned: i,
		})
	}

	s := newMetricsRAGTestServer(rec)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/rag", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec2 := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("M11: expected 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}

	var resp struct {
		Aggregates   metrics.Aggregates `json:"aggregates"`
		RecentEvents []metrics.Event    `json:"recent_events"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&resp); err != nil {
		t.Fatalf("M11: decode response: %v", err)
	}
	if len(resp.RecentEvents) != 10 {
		t.Errorf("M11: want 10 recent_events, got %d", len(resp.RecentEvents))
	}
	// Events newest-first: last recorded (TotalDurationMs=100) should be at index 0.
	if resp.RecentEvents[0].TotalDurationMs != 100 {
		t.Errorf("M11: first event (newest) should have TotalDurationMs=100, got %d",
			resp.RecentEvents[0].TotalDurationMs)
	}
	// Aggregates should be non-zero.
	if resp.Aggregates.TotalDurationMs.Avg == 0 {
		t.Error("M11: aggregates.total_duration_ms.avg should be non-zero")
	}
}

// ---------------------------------------------------------------------------
// M12: GET /api/metrics/rag when RAGMetrics == nil returns 501.
// ---------------------------------------------------------------------------

func TestHandleGetRAGMetrics_NilRecorder_Returns501(t *testing.T) {
	s := newMetricsRAGTestServer(nil) // nil recorder

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/rag", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("M12: expected 501, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if body == "" {
		t.Error("M12: expected non-empty body indicating 'metrics not configured'")
	}
}

// ---------------------------------------------------------------------------
// Additional: auth required (401 when no token).
// ---------------------------------------------------------------------------

func TestHandleGetRAGMetrics_AuthRequired(t *testing.T) {
	rec := metrics.NewRingRecorder(10)
	s := newMetricsRAGTestServer(rec)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/rag", nil)
	// No Authorization header.
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("M11-auth: expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Additional: ring cap respected — recorder with cap=3 shows only 3 events.
// ---------------------------------------------------------------------------

func TestHandleGetRAGMetrics_RingCapRespected(t *testing.T) {
	rec := metrics.NewRingRecorder(3)
	for i := 0; i < 7; i++ {
		rec.Record(metrics.Event{
			Timestamp:       time.Now(),
			TotalDurationMs: int64(i),
		})
	}

	s := newMetricsRAGTestServer(rec)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics/rag", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ring-cap: expected 200, got %d", rr.Code)
	}

	var resp struct {
		RecentEvents []metrics.Event `json:"recent_events"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("ring-cap: decode: %v", err)
	}
	if len(resp.RecentEvents) != 3 {
		t.Errorf("ring-cap: want 3 recent_events (cap), got %d", len(resp.RecentEvents))
	}
}
