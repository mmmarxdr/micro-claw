package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ─── ExtractKeywords tests ─────────────────────────────────────────────────────

func TestExtractKeywords_StripStopWords(t *testing.T) {
	kws := ExtractKeywords("what is the best way to use golang")
	for _, kw := range kws {
		if stopWords[kw] {
			t.Errorf("stop word %q should have been stripped", kw)
		}
	}
	// "golang" should survive
	found := false
	for _, kw := range kws {
		if kw == "golang" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected keyword 'golang' to be present, got %v", kws)
	}
}

func TestExtractKeywords_RemoveShortTokens(t *testing.T) {
	kws := ExtractKeywords("go is a language")
	for _, kw := range kws {
		if len(kw) < 3 {
			t.Errorf("short token %q (len %d) should have been removed", kw, len(kw))
		}
	}
}

func TestExtractKeywords_Deduplicates(t *testing.T) {
	kws := ExtractKeywords("golang golang golang")
	if len(kws) != 1 {
		t.Errorf("expected 1 unique keyword 'golang', got %d: %v", len(kws), kws)
	}
	if len(kws) == 1 && kws[0] != "golang" {
		t.Errorf("expected 'golang', got %q", kws[0])
	}
}

func TestExtractKeywords_CaseNormalization(t *testing.T) {
	kws := ExtractKeywords("Golang GOLANG GoLang")
	if len(kws) != 1 {
		t.Errorf("expected 1 keyword after case-folding, got %d: %v", len(kws), kws)
	}
}

func TestExtractKeywords_PreservesCodeIdentifiers(t *testing.T) {
	kws := ExtractKeywords("auth_token get-config some_func")
	expected := []string{"auth_token", "get-config", "some_func"}
	for _, want := range expected {
		found := false
		for _, kw := range kws {
			if kw == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected code identifier %q in keywords %v", want, kws)
		}
	}
}

func TestExtractKeywords_EmptyInput(t *testing.T) {
	kws := ExtractKeywords("")
	if len(kws) != 0 {
		t.Errorf("expected empty keywords for empty input, got %v", kws)
	}
}

func TestExtractKeywords_AllStopWords(t *testing.T) {
	kws := ExtractKeywords("what is the a an or but")
	if len(kws) != 0 {
		t.Errorf("expected no keywords from all stop words, got %v", kws)
	}
}

// ─── BuildFTSQuery tests ───────────────────────────────────────────────────────

func TestBuildFTSQuery_BasicQuery(t *testing.T) {
	q := BuildFTSQuery("golang memory search")
	if q == "" {
		t.Fatal("expected non-empty FTS query")
	}
	// Each keyword should be double-quoted.
	if !strings.Contains(q, `"golang"`) {
		t.Errorf("expected 'golang' quoted in FTS query, got %q", q)
	}
	if !strings.Contains(q, `"memory"`) {
		t.Errorf("expected 'memory' quoted in FTS query, got %q", q)
	}
	if !strings.Contains(q, `"search"`) {
		t.Errorf("expected 'search' quoted in FTS query, got %q", q)
	}
	// Keywords joined with OR.
	if !strings.Contains(q, " OR ") {
		t.Errorf("expected OR between keywords in FTS query, got %q", q)
	}
}

func TestBuildFTSQuery_AllStopWordsReturnsEmpty(t *testing.T) {
	q := BuildFTSQuery("what is the")
	if q != "" {
		t.Errorf("expected empty FTS query for all-stop-word input, got %q", q)
	}
}

func TestBuildFTSQuery_SingleKeyword(t *testing.T) {
	q := BuildFTSQuery("authentication")
	expected := `"authentication"`
	if q != expected {
		t.Errorf("expected %q, got %q", expected, q)
	}
}

func TestBuildFTSQuery_EmptyInputReturnsEmpty(t *testing.T) {
	q := BuildFTSQuery("")
	if q != "" {
		t.Errorf("expected empty FTS query for empty input, got %q", q)
	}
}

func TestBuildFTSQuery_CodeIdentifiers(t *testing.T) {
	q := BuildFTSQuery("search auth_token config")
	if !strings.Contains(q, `"auth_token"`) {
		t.Errorf("expected auth_token in FTS query, got %q", q)
	}
}

// ─── SQLiteStore SearchMemory enhancement tests ───────────────────────────────

func TestSQLiteStore_SearchMemory_KeywordExtraction(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entries := []MemoryEntry{
		{ID: "m1", Content: "authentication token expired", CreatedAt: mustParseTime("2024-01-01T10:00:00Z")},
		{ID: "m2", Content: "database connection pooling", CreatedAt: mustParseTime("2024-01-02T10:00:00Z")},
		{ID: "m3", Content: "user authentication system", CreatedAt: mustParseTime("2024-01-03T10:00:00Z")},
	}
	for _, e := range entries {
		if err := s.AppendMemory(ctx, "scope", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	// "the authentication" — "the" is a stop word, "authentication" is the keyword.
	results, err := s.SearchMemory(ctx, "scope", "the authentication", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results for 'authentication', got %d", len(results))
	}
}

func TestSQLiteStore_SearchMemory_AllStopWordsFallbackToRecency(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	entries := []MemoryEntry{
		{ID: "old", Content: "what is a thing", CreatedAt: mustParseTime("2024-01-01T00:00:00Z")},
		{ID: "new", Content: "what is the thing", CreatedAt: mustParseTime("2024-06-01T00:00:00Z")},
	}
	for _, e := range entries {
		if err := s.AppendMemory(ctx, "scope", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	// "what is" — both tokens are stop words, so FTS query is empty.
	// LIKE fallback should still find both entries (they contain "what is");
	// newest should come first due to recency ordering.
	results, err := s.SearchMemory(ctx, "scope", "what is", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result, got 0")
	}
	// Newest entry should appear first in the fallback recency order.
	if results[0].ID != "new" {
		t.Errorf("expected newest entry first in recency fallback, got %q", results[0].ID)
	}
}

func TestSQLiteStore_SearchMemory_RecencyBoost(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Both entries contain "golang" the same number of times in the same field.
	// The newer one should rank first due to recency weighting.
	entries := []MemoryEntry{
		{ID: "older", Content: "golang development patterns", CreatedAt: mustParseTime("2020-01-01T00:00:00Z")},
		{ID: "newer", Content: "golang development patterns", CreatedAt: mustParseTime("2024-06-01T00:00:00Z")},
	}
	for _, e := range entries {
		if err := s.AppendMemory(ctx, "scope", e); err != nil {
			t.Fatalf("AppendMemory: %v", err)
		}
	}

	results, err := s.SearchMemory(ctx, "scope", "golang", 10)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "newer" {
		t.Errorf("expected newer entry ranked first due to recency boost, got %q first", results[0].ID)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustParseTime: " + err.Error())
	}
	return t
}
