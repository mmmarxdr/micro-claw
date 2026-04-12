package web

import (
	"net/http"
	"strconv"

	"microagent/internal/audit"
)

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	type metricsResponse struct {
		TokensToday       int64   `json:"tokens_today"`
		CostToday         float64 `json:"cost_today"`
		TokensMonth       int64   `json:"tokens_month"`
		CostMonth         float64 `json:"cost_month"`
		ConversationCount int     `json:"conversation_count"`
		MemoryCount       int     `json:"memory_count"`
	}

	var resp metricsResponse

	if ar, ok := s.deps.Auditor.(audit.AuditReader); ok {
		today, _ := ar.TodayMetrics(r.Context())
		resp.TokensToday = today.TotalTokens
		resp.CostToday = today.EstimatedCost

		history, _ := ar.MetricsHistory(r.Context(), 30)
		for _, d := range history {
			resp.TokensMonth += d.TotalTokens
			resp.CostMonth += d.EstimatedCost
		}
	}

	entries, _ := s.deps.Store.SearchMemory(r.Context(), "", "", 0)
	resp.MemoryCount = len(entries)

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetMetricsHistory(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}

	ar, ok := s.deps.Auditor.(audit.AuditReader)
	if !ok {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	history, err := ar.MetricsHistory(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, history)
}
