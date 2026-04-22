//go:build eval

package rag_test

// T9: Regression guard — search_docs tool path runs same eval contract as eval_test.go
//
// Run with:
//
//	go test -tags=eval -run TestEval_ToolPath ./internal/rag/...
//
// Requires a real DB at ~/.daimon/data/daimon.db with the expected docs indexed.
// The test skips when the DB is absent or the expected docs are not found.
//
// Why this test exists:
//   The original HyDE bug was undetected because eval_test.go only tested the
//   SearchChunks path directly, not the search_docs tool code path. This test
//   runs eval queries through the tool (like the LLM does) so the same bug
//   would cause a failure at the tool level before a PR is merged.
//
// Contract: same "never-worse-on-lexical" as T37 in eval_test.go, but exercised
//   via BuildRAGTools + search_docs.Execute, not SearchChunks directly.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"daimon/internal/rag"
)

// TestEval_ToolPath_NeverWorseOnLexical (T9) runs every eval query through
// the search_docs tool and enforces the lexical non-regression contract.
func TestEval_ToolPath_NeverWorseOnLexical(t *testing.T) {
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

	// Verify expected docs exist.
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
			t.Skipf("expected doc %q not found in DB; skipping (run manual labeling gate first)", substr)
		}
	}

	store := rag.NewSQLiteDocumentStore(db, 500, 100000)

	// Build the tool in baseline (HyDE disabled) mode — same as the original buggy code.
	// This gives us the "correct" top-1 for lexical queries.
	baselineDeps := rag.RAGToolDeps{
		Store:    store,
		HydeConf: rag.HydeSearchConfig{Enabled: false},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}
	baselineTools := rag.BuildRAGTools(baselineDeps)
	baselineTool := findSearchTool(t, baselineTools)

	// Build the tool in HyDE-enabled mode. Use the query as proxy hypothesis
	// (same approach as eval_test.go — conservative but tests the plumbing).
	hydeDeps := rag.RAGToolDeps{
		Store: store,
		HypothesisFn: func(_ context.Context, q string) (string, error) {
			return q, nil // proxy hypothesis
		},
		HydeConf: rag.HydeSearchConfig{
			Enabled:       true,
			QueryWeight:   0.3,
			MaxCandidates: 20,
		},
		RetrievalConf: rag.RetrievalSearchConfig{Limit: 5},
	}
	hydeTools := rag.BuildRAGTools(hydeDeps)
	hydeTool := findSearchTool(t, hydeTools)

	isLexical := func(qType string) bool {
		return qType == "lexical" || qType == "edge_short"
	}

	for _, q := range queries {
		baseResult := toolSearchResult(t, baselineTool, q.Query)
		hydeResult := toolSearchResult(t, hydeTool, q.Query)

		baseTop1 := firstTitle(baseResult)
		hydeTop1 := firstTitle(hydeResult)

		t.Logf("query %2d [%-15s] baseline-top1=%q  hyde-top1=%q  q=%q",
			q.ID, q.Type, baseTop1, hydeTop1, q.Query)

		// T9 contract: lexical queries must not regress when baseline was correct.
		if isLexical(q.Type) {
			baselineCorrect := strings.Contains(baseTop1, q.ExpectedDocTitleSubstr)
			hydeCorrect := strings.Contains(hydeTop1, q.ExpectedDocTitleSubstr)
			if baselineCorrect && !hydeCorrect {
				t.Errorf(
					"T9 REGRESSION (tool path): query %d [%s] %q — baseline top-1 correct (%q) but hyde top-1 wrong (%q). "+
						"This is the exact bug that HyDE was supposed to fix but broken the tool path.",
					q.ID, q.Type, q.Query, baseTop1, hydeTop1,
				)
			}
		}
	}
}

// toolSearchResult runs the search_docs tool and parses the first result title.
func toolSearchResult(t *testing.T, tool rag.Tool, query string) rag.ToolResult {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"query": query, "top_k": 5})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("tool.Execute error: %v", err)
	}
	return result
}

// firstTitle extracts the document title from the first line of a search result.
// Format: "1. [Title] (score: ...)"
func firstTitle(result rag.ToolResult) string {
	if result.IsError || result.Content == "" {
		return ""
	}
	line := strings.SplitN(result.Content, "\n", 2)[0]
	// Extract [Title] from "1. [Title] (score: ...)"
	start := strings.Index(line, "[")
	end := strings.Index(line, "]")
	if start >= 0 && end > start {
		return line[start+1 : end]
	}
	return line
}
