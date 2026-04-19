package web

import (
	"context"
	"net/http"
	"strconv"

	"daimon/internal/audit"
	"daimon/internal/store"
)

// metricsDay matches the frontend MetricsSnapshot.today / month shape.
type metricsDay struct {
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	Conversations int     `json:"conversations"`
	Messages      int     `json:"messages"`
}

// metricsMonth matches MetricsSnapshot.month (no conversations/messages).
type metricsMonth struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// metricsHistoryEntry matches MetricsSnapshot.history[].
type metricsHistoryEntry struct {
	Date         string  `json:"date"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// metricsSnapshot is the full shape the frontend expects for MetricsSnapshot.
type metricsSnapshot struct {
	Today   metricsDay            `json:"today"`
	Month   metricsMonth          `json:"month"`
	History []metricsHistoryEntry `json:"history"`
}

// buildMetricsSnapshot constructs the full MetricsSnapshot for the given context.
func (s *Server) buildMetricsSnapshot(ctx context.Context) metricsSnapshot {
	snap := metricsSnapshot{
		History: []metricsHistoryEntry{},
	}

	if ar, ok := s.deps.Auditor.(audit.AuditReader); ok {
		today, _ := ar.TodayMetrics(ctx)
		snap.Today.InputTokens = today.InputTokens
		snap.Today.OutputTokens = today.OutputTokens
		snap.Today.CostUSD = today.EstimatedCost

		history, _ := ar.MetricsHistory(ctx, 30)
		for _, d := range history {
			snap.Month.InputTokens += d.InputTokens
			snap.Month.OutputTokens += d.OutputTokens
			snap.Month.CostUSD += d.EstimatedCost
			snap.History = append(snap.History, metricsHistoryEntry{
				Date:         d.Date,
				InputTokens:  d.InputTokens,
				OutputTokens: d.OutputTokens,
				CostUSD:      d.EstimatedCost,
			})
		}
	}

	if ws, ok := s.deps.Store.(store.WebStore); ok {
		snap.Today.Conversations, _ = ws.CountConversations(ctx, "")
	}

	entries, _ := s.deps.Store.SearchMemory(ctx, "", "", 0)
	snap.Today.Messages = len(entries)

	return snap
}

func (s *Server) handleGetMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildMetricsSnapshot(r.Context()))
}

func (s *Server) handleGetMetricsHistory(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}

	snap := metricsSnapshot{
		History: []metricsHistoryEntry{},
	}

	ar, ok := s.deps.Auditor.(audit.AuditReader)
	if !ok {
		writeJSON(w, http.StatusOK, snap)
		return
	}

	history, err := ar.MetricsHistory(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	for _, d := range history {
		snap.Month.InputTokens += d.InputTokens
		snap.Month.OutputTokens += d.OutputTokens
		snap.Month.CostUSD += d.EstimatedCost
		snap.History = append(snap.History, metricsHistoryEntry{
			Date:         d.Date,
			InputTokens:  d.InputTokens,
			OutputTokens: d.OutputTokens,
			CostUSD:      d.EstimatedCost,
		})
	}

	// today is the last entry in the history if present
	if len(history) > 0 {
		last := history[len(history)-1]
		snap.Today.InputTokens = last.InputTokens
		snap.Today.OutputTokens = last.OutputTokens
		snap.Today.CostUSD = last.EstimatedCost
	}

	writeJSON(w, http.StatusOK, snap)
}
