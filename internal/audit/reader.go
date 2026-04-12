package audit

import (
	"context"
	"fmt"
	"time"
)

// AuditReader provides read-only access to aggregated audit data.
// Only SQLiteAuditor implements this interface; callers should type-assert
// rather than embedding AuditReader in Auditor.
type AuditReader interface {
	TodayMetrics(ctx context.Context) (DailyMetrics, error)
	MetricsHistory(ctx context.Context, days int) ([]DailyMetrics, error)
}

// DailyMetrics holds aggregated token and cost data for a single calendar day.
type DailyMetrics struct {
	Date          string  `json:"date"` // "2026-04-12"
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	TotalTokens   int64   `json:"total_tokens"`
	EstimatedCost float64 `json:"estimated_cost"` // USD
	RequestCount  int     `json:"request_count"`
}

// TodayMetrics returns aggregated LLM call metrics for the current UTC day.
//
// Timestamps are stored as RFC3339 strings; we match on substr(timestamp,1,10)
// to extract the YYYY-MM-DD prefix reliably across all SQLite versions.
func (a *SQLiteAuditor) TodayMetrics(ctx context.Context) (DailyMetrics, error) {
	today := time.Now().UTC().Format("2006-01-02")

	row := a.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)), 0),
			COUNT(*)
		FROM audit_events
		WHERE event_type = 'llm_call'
		  AND substr(timestamp, 1, 10) = ?
	`, today)

	var inputTokens, outputTokens, totalTokens int64
	var requestCount int
	if err := row.Scan(&inputTokens, &outputTokens, &totalTokens, &requestCount); err != nil {
		return DailyMetrics{}, fmt.Errorf("audit: TodayMetrics scan: %w", err)
	}

	cost, err := a.estimateDayCost(ctx, today)
	if err != nil {
		return DailyMetrics{}, err
	}

	return DailyMetrics{
		Date:          today,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   totalTokens,
		EstimatedCost: cost,
		RequestCount:  requestCount,
	}, nil
}

// MetricsHistory returns one DailyMetrics per calendar day for the last [days] days
// (inclusive of today, in UTC). Missing days are zero-filled.
func (a *SQLiteAuditor) MetricsHistory(ctx context.Context, days int) ([]DailyMetrics, error) {
	if days <= 0 {
		return nil, nil
	}

	// Build the inclusive start date for the window.
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -(days - 1))
	startDate := start.Format("2006-01-02")

	rows, err := a.db.QueryContext(ctx, `
		SELECT
			substr(timestamp, 1, 10)              AS day,
			COALESCE(SUM(input_tokens), 0)        AS input_tokens,
			COALESCE(SUM(output_tokens), 0)       AS output_tokens,
			COALESCE(SUM(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)), 0) AS total_tokens,
			COUNT(*)                              AS request_count
		FROM audit_events
		WHERE event_type = 'llm_call'
		  AND substr(timestamp, 1, 10) >= ?
		GROUP BY substr(timestamp, 1, 10)
		ORDER BY day
	`, startDate)
	if err != nil {
		return nil, fmt.Errorf("audit: MetricsHistory query: %w", err)
	}
	defer rows.Close()

	type dbRow struct {
		inputTokens  int64
		outputTokens int64
		totalTokens  int64
		requestCount int
	}
	byDay := make(map[string]dbRow)
	for rows.Next() {
		var day string
		var r dbRow
		if err := rows.Scan(&day, &r.inputTokens, &r.outputTokens, &r.totalTokens, &r.requestCount); err != nil {
			return nil, fmt.Errorf("audit: MetricsHistory scan: %w", err)
		}
		byDay[day] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: MetricsHistory rows: %w", err)
	}

	// Fetch per-model cost breakdowns for the date range.
	costByDay, err := a.estimateRangeCosts(ctx, startDate)
	if err != nil {
		return nil, err
	}

	// Build the result slice, zero-filling days with no events.
	result := make([]DailyMetrics, 0, days)
	for i := range days {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		r := byDay[day] // zero-value if missing
		result = append(result, DailyMetrics{
			Date:          day,
			InputTokens:   r.inputTokens,
			OutputTokens:  r.outputTokens,
			TotalTokens:   r.totalTokens,
			EstimatedCost: costByDay[day],
			RequestCount:  r.requestCount,
		})
	}
	return result, nil
}

// estimateDayCost sums per-model cost for a single day (YYYY-MM-DD string).
func (a *SQLiteAuditor) estimateDayCost(ctx context.Context, day string) (float64, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT model, SUM(input_tokens), SUM(output_tokens)
		FROM audit_events
		WHERE event_type = 'llm_call'
		  AND substr(timestamp, 1, 10) = ?
		  AND model IS NOT NULL AND model != ''
		GROUP BY model
	`, day)
	if err != nil {
		return 0, fmt.Errorf("audit: estimateDayCost query: %w", err)
	}
	defer rows.Close()

	var total float64
	for rows.Next() {
		var model string
		var input, output int64
		if err := rows.Scan(&model, &input, &output); err != nil {
			return 0, fmt.Errorf("audit: estimateDayCost scan: %w", err)
		}
		total += EstimateCost(model, input, output)
	}
	return total, rows.Err()
}

// estimateRangeCosts returns a map[date]cost for all days >= startDate (YYYY-MM-DD).
func (a *SQLiteAuditor) estimateRangeCosts(ctx context.Context, startDate string) (map[string]float64, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT substr(timestamp, 1, 10) AS day, model, SUM(input_tokens), SUM(output_tokens)
		FROM audit_events
		WHERE event_type = 'llm_call'
		  AND substr(timestamp, 1, 10) >= ?
		  AND model IS NOT NULL AND model != ''
		GROUP BY substr(timestamp, 1, 10), model
	`, startDate)
	if err != nil {
		return nil, fmt.Errorf("audit: estimateRangeCosts query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var day, model string
		var input, output int64
		if err := rows.Scan(&day, &model, &input, &output); err != nil {
			return nil, fmt.Errorf("audit: estimateRangeCosts scan: %w", err)
		}
		result[day] += EstimateCost(model, input, output)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: estimateRangeCosts rows: %w", err)
	}
	return result, nil
}

// Compile-time interface assertion.
var _ AuditReader = (*SQLiteAuditor)(nil)
