package web

import (
	"net/http"

	"daimon/internal/rag/metrics"
)

// ragMetricsResponse is the JSON shape for GET /api/metrics/rag.
type ragMetricsResponse struct {
	Aggregates   metrics.Aggregates `json:"aggregates"`
	RecentEvents []metrics.Event    `json:"recent_events"`
}

// handleGetRAGMetrics serves GET /api/metrics/rag.
//
// Returns 501 when the RAGMetrics recorder is not configured.
// Returns 200 + JSON with aggregates and recent events (newest-first) otherwise.
//
// Auth is enforced by the existing authMiddlewareDynamic — no per-handler check.
func (s *Server) handleGetRAGMetrics(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.deps.RAGMetrics.(*metrics.RingRecorder)
	if !ok || rec == nil {
		// Also handle the case where RAGMetrics is a non-nil interface wrapping nil.
		if s.deps.RAGMetrics == nil {
			writeError(w, http.StatusNotImplemented, "metrics not configured")
			return
		}
		// If it's set but not a RingRecorder, treat as generic Recorder (no Aggregates).
		// For now write 501 — only RingRecorder exposes Aggregates().
		writeError(w, http.StatusNotImplemented, "metrics not configured")
		return
	}

	snap := rec.Snapshot()

	// Reverse slice for newest-first ordering.
	newestFirst := make([]metrics.Event, len(snap))
	for i, e := range snap {
		newestFirst[len(snap)-1-i] = e
	}

	agg := rec.Aggregates()

	writeJSON(w, http.StatusOK, ragMetricsResponse{
		Aggregates:   agg,
		RecentEvents: newestFirst,
	})
}
