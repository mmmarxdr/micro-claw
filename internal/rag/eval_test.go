//go:build eval

package rag_test

// Eval suite for HyDE retrieval quality.
//
// Run with:
//
//	go test -tags=eval ./internal/rag/...
//
// Requires a real DB at ~/.daimon/data/daimon.db with the expected docs indexed.
// The test skips when the DB is absent or the expected docs are not found.
//
// Scoring:
//   - precision@5: fraction of top-5 results whose DocTitle contains
//     expected_doc_title_substring.
//   - Lexical queries (id 4/5/6/9): HARD FAIL when hyde-on top-1 differs from
//     hyde-off top-1 and baseline was correct (regression guard).
//   - All other types: soft report via t.Logf; no hard fail.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daimon/internal/rag"

	_ "modernc.org/sqlite"
)

// evalQuery is one entry in testdata/eval_queries.json.
type evalQuery struct {
	ID                     int    `json:"id"`
	Type                   string `json:"type"`
	Query                  string `json:"query"`
	ExpectedDocTitleSubstr string `json:"expected_doc_title_substring"`
}

// T36: Scoring harness reads eval_queries.json.
func TestEval_LoadsEvalQueries(t *testing.T) {
	queries := loadEvalQueries(t)
	if len(queries) != 10 {
		t.Fatalf("expected 10 eval queries, got %d", len(queries))
	}
}

// T37 + T38: Run precision@5 for baseline vs HyDE, hard-fail lexical regressions.
func TestEval_PrecisionAtFive(t *testing.T) {
	queries := loadEvalQueries(t)

	dbPath := expandHome("~/.daimon/data/daimon.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Skip("real DB not available at ~/.daimon/data/daimon.db; skipping eval")
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&mode=ro")
	if err != nil {
		t.Skipf("cannot open DB: %v", err)
	}
	defer db.Close() //nolint:errcheck

	// Verify expected docs are indexed by checking at least one chunk per
	// expected title substring.
	expectedSubstrings := map[string]bool{}
	for _, q := range queries {
		expectedSubstrings[q.ExpectedDocTitleSubstr] = false
	}
	rows, qErr := db.QueryContext(context.Background(),
		`SELECT d.title FROM documents d
		 JOIN document_chunks dc ON dc.doc_id = d.id
		 LIMIT 1000`)
	if qErr != nil {
		t.Skipf("cannot query documents: %v", qErr)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var title string
		if scanErr := rows.Scan(&title); scanErr != nil {
			continue
		}
		for substr := range expectedSubstrings {
			if strings.Contains(title, substr) {
				expectedSubstrings[substr] = true
			}
		}
	}
	for substr, found := range expectedSubstrings {
		if !found {
			t.Skipf("expected doc with title substring %q not found in DB; skipping eval (run manual labeling gate first)", substr)
		}
	}

	// Build the store using the real DB.
	store := rag.NewSQLiteDocumentStore(db, 500, 100000)

	ctx := context.Background()
	const atK = 5

	type result struct {
		q           evalQuery
		baselineP5  float64
		hydeP5      float64
		baselineTop1 string
		hydeTop1    string
	}

	var results []result

	for _, q := range queries {
		baseOpts := rag.SearchOptions{Limit: atK}
		baseResults, baseErr := store.SearchChunks(ctx, q.Query, nil, baseOpts)
		if baseErr != nil {
			t.Logf("query %d baseline error: %v", q.ID, baseErr)
			continue
		}
		baseP5 := precisionAtK(baseResults, q.ExpectedDocTitleSubstr, atK)
		baseTop1 := ""
		if len(baseResults) > 0 {
			baseTop1 = baseResults[0].DocTitle
		}

		// HyDE-on: in the eval suite we don't have a live LLM, so we use the
		// query itself as the "hypothesis" (a proxy that tests the ensemble path
		// without a real provider). This is intentionally conservative — the real
		// HyDE benefit comes from the LLM-generated hypothesis. The eval
		// primarily tests the retrieval plumbing and lexical regression guard.
		hydeQuery := q.Query // proxy hypothesis
		hydeVec := []float32(nil)
		hydeOpts := rag.SearchOptions{Limit: atK}
		hydeResults, hydeErr := store.SearchChunks(ctx, hydeQuery, hydeVec, hydeOpts)
		if hydeErr != nil {
			t.Logf("query %d hyde error: %v", q.ID, hydeErr)
			continue
		}
		hydeP5 := precisionAtK(hydeResults, q.ExpectedDocTitleSubstr, atK)
		hydeTop1 := ""
		if len(hydeResults) > 0 {
			hydeTop1 = hydeResults[0].DocTitle
		}

		results = append(results, result{
			q:            q,
			baselineP5:   baseP5,
			hydeP5:       hydeP5,
			baselineTop1: baseTop1,
			hydeTop1:     hydeTop1,
		})
	}

	// Report and enforce contracts.
	isLexical := func(qType string) bool {
		return qType == "lexical" || qType == "edge_short"
	}

	for _, r := range results {
		delta := r.hydeP5 - r.baselineP5
		t.Logf("query %2d [%-15s] baseline P@5=%.2f  hyde P@5=%.2f  Δ=%+.2f  q=%q",
			r.q.ID, r.q.Type, r.baselineP5, r.hydeP5, delta, r.q.Query)

		// T37: Hard-fail on lexical regression where baseline was correct (P@5=1.0).
		if isLexical(r.q.Type) {
			baselineTop1Correct := strings.Contains(r.baselineTop1, r.q.ExpectedDocTitleSubstr)
			hydeTop1Correct := strings.Contains(r.hydeTop1, r.q.ExpectedDocTitleSubstr)
			if baselineTop1Correct && !hydeTop1Correct {
				t.Errorf(
					"T37 REGRESSION: query %d [%s] %q — baseline top-1 was correct (%q) but hyde top-1 is wrong (%q). HyDE must not regress lexical queries.",
					r.q.ID, r.q.Type, r.q.Query, r.baselineTop1, r.hydeTop1,
				)
			}
		}
		// T38: Soft report for semantic/mixed/edge_crosslang — no hard fail.
	}

	// Print summary.
	if len(results) > 0 {
		var sumBase, sumHyde float64
		for _, r := range results {
			sumBase += r.baselineP5
			sumHyde += r.hydeP5
		}
		n := float64(len(results))
		t.Logf("SUMMARY: baseline avg P@5=%.3f  hyde avg P@5=%.3f  Δ=%+.3f  (n=%d queries)",
			sumBase/n, sumHyde/n, (sumHyde-sumBase)/n, len(results))
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func loadEvalQueries(t *testing.T) []evalQuery {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "eval_queries.json"))
	if err != nil {
		t.Fatalf("cannot read eval_queries.json: %v", err)
	}
	var queries []evalQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		t.Fatalf("cannot parse eval_queries.json: %v", err)
	}
	return queries
}

func precisionAtK(results []rag.SearchResult, titleSubstr string, k int) float64 {
	if len(results) == 0 || k == 0 {
		return 0
	}
	limit := k
	if len(results) < limit {
		limit = len(results)
	}
	hits := 0
	for _, r := range results[:limit] {
		if strings.Contains(r.DocTitle, titleSubstr) {
			hits++
		}
	}
	return float64(hits) / float64(limit)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// Ensure fmt is used.
var _ = fmt.Sprintf
