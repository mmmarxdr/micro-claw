package store

import (
	"context"
	"testing"
	"time"

	"microagent/internal/config"
)

// TestSQLiteStore_OutputStore_IndexOutput tests that IndexOutput correctly
// stores a tool output for later search.
func TestSQLiteStore_OutputStore_IndexOutput(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	output := ToolOutput{
		ID:        "output-001",
		ToolName:  "shell",
		Command:   "echo hello",
		Content:   "hello world",
		Truncated: false,
		ExitCode:  0,
		Timestamp: now,
	}

	if err := s.IndexOutput(ctx, output); err != nil {
		t.Fatalf("IndexOutput failed: %v", err)
	}

	// Verify the output can be found via search
	results, err := s.SearchOutputs(ctx, "hello", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "output-001" {
		t.Errorf("expected ID output-001, got %s", results[0].ID)
	}
}

// TestSQLiteStore_OutputStore_SearchOutputs tests that SearchOutputs correctly
// searches indexed tool outputs using FTS5.
func TestSQLiteStore_OutputStore_SearchOutputs(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Index multiple outputs
	outputs := []ToolOutput{
		{
			ID:        "output-001",
			ToolName:  "shell",
			Command:   "echo hello",
			Content:   "hello world from shell",
			Truncated: false,
			ExitCode:  0,
			Timestamp: now,
		},
		{
			ID:        "output-002",
			ToolName:  "http",
			Command:   "GET /api/users",
			Content:   "user data in JSON format",
			Truncated: false,
			ExitCode:  200,
			Timestamp: now,
		},
		{
			ID:        "output-003",
			ToolName:  "shell",
			Command:   "ls -la",
			Content:   "total 4096 drwxr-xr-x  12 root root 4096 Apr  9 12:00 .",
			Truncated: false,
			ExitCode:  0,
			Timestamp: now,
		},
	}

	for _, o := range outputs {
		if err := s.IndexOutput(ctx, o); err != nil {
			t.Fatalf("IndexOutput failed for %s: %v", o.ID, err)
		}
	}

	// Search for "hello" - should return output-001
	results, err := s.SearchOutputs(ctx, "hello", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'hello', got %d", len(results))
	}
	if results[0].ID != "output-001" {
		t.Errorf("expected output-001, got %s", results[0].ID)
	}

	// Search for "JSON" - should return output-002
	results, err = s.SearchOutputs(ctx, "JSON", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'JSON', got %d", len(results))
	}
	if results[0].ID != "output-002" {
		t.Errorf("expected output-002, got %s", results[0].ID)
	}

	// Search for "root" - should return output-003
	results, err = s.SearchOutputs(ctx, "root", 10)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'root', got %d", len(results))
	}
	if results[0].ID != "output-003" {
		t.Errorf("expected output-003, got %s", results[0].ID)
	}

	// Test limit parameter
	results, err = s.SearchOutputs(ctx, "*", 2)
	if err != nil {
		t.Fatalf("SearchOutputs failed: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(results))
	}
}

// TestSQLiteStore_OutputStore_EmptySearch tests search behavior with empty query
func TestSQLiteStore_OutputStore_EmptySearch(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Index an output
	output := ToolOutput{
		ID:        "output-001",
		ToolName:  "shell",
		Command:   "echo test",
		Content:   "some test content",
		Truncated: false,
		ExitCode:  0,
		Timestamp: now,
	}
	if err := s.IndexOutput(ctx, output); err != nil {
		t.Fatalf("IndexOutput failed: %v", err)
	}

	// Empty search should return all results
	results, err := s.SearchOutputs(ctx, "", 10)
	if err != nil {
		t.Fatalf("SearchOutputs with empty query failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result for empty search, got %d", len(results))
	}
}

// ─── M4: IndexOutput validation ──────────────────────────────────────────────

func TestIndexOutput_EmptyID_ReturnsError(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	err = s.IndexOutput(context.Background(), ToolOutput{
		ID:       "",
		ToolName: "shell",
		Content:  "some output",
	})
	if err != ErrOutputMissingID {
		t.Errorf("expected ErrOutputMissingID, got %v", err)
	}
}

func TestIndexOutput_EmptyToolName_ReturnsError(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	err = s.IndexOutput(context.Background(), ToolOutput{
		ID:       "some-id",
		ToolName: "",
		Content:  "some output",
	})
	if err != ErrOutputMissingToolName {
		t.Errorf("expected ErrOutputMissingToolName, got %v", err)
	}
}

func TestIndexOutput_EmptyContent_ReturnsError(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	err = s.IndexOutput(context.Background(), ToolOutput{
		ID:       "some-id",
		ToolName: "shell",
		Content:  "",
	})
	if err != ErrOutputEmptyContent {
		t.Errorf("expected ErrOutputEmptyContent, got %v", err)
	}
}

func TestIndexOutput_AllFieldsValid_Succeeds(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	err = s.IndexOutput(context.Background(), ToolOutput{
		ID:        "valid-id",
		ToolName:  "shell",
		Content:   "valid content",
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// ─── M3: LIKE wildcard escaping ───────────────────────────────────────────────

// TestSearchOutputs_LIKEFallback_EscapesWildcards verifies that literal % and _
// characters in the user query are not treated as SQL LIKE wildcards. The test
// inserts two outputs — one containing a literal percent sign, one without —
// and checks that searching for the exact "50%" string returns only the match.
func TestSearchOutputs_LIKEFallback_EscapesWildcards(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	outputs := []ToolOutput{
		{
			ID:        "pct-match",
			ToolName:  "shell",
			Content:   "disk usage 50% full",
			Timestamp: now,
		},
		{
			ID:        "no-match",
			ToolName:  "shell",
			Content:   "disk usage 5000 blocks",
			Timestamp: now.Add(-time.Second),
		},
	}
	for _, o := range outputs {
		if err := s.IndexOutput(ctx, o); err != nil {
			t.Fatalf("IndexOutput(%s): %v", o.ID, err)
		}
	}

	// The query "50%" should match only "disk usage 50% full", not "5000 blocks"
	// (which would match if '%' were treated as a wildcard).
	// BuildFTSQuery returns "" for "50%" so the LIKE path is exercised.
	results, err := s.SearchOutputs(ctx, "50%", 10)
	if err != nil {
		t.Fatalf("SearchOutputs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "pct-match" {
		t.Errorf("expected pct-match, got %s", results[0].ID)
	}
}

// TestSearchOutputs_LIKEFallback_EscapesUnderscore verifies that literal _ in
// the query is not treated as a single-character wildcard.
func TestSearchOutputs_LIKEFallback_EscapesUnderscore(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	outputs := []ToolOutput{
		{
			ID:        "under-match",
			ToolName:  "shell",
			Content:   "var foo_bar = 1",
			Timestamp: now,
		},
		{
			ID:        "no-under-match",
			ToolName:  "shell",
			Content:   "var fooXbar = 1",
			Timestamp: now.Add(-time.Second),
		},
	}
	for _, o := range outputs {
		if err := s.IndexOutput(ctx, o); err != nil {
			t.Fatalf("IndexOutput(%s): %v", o.ID, err)
		}
	}

	// "foo_bar" with escaped _ should match only the literal underscore variant.
	results, err := s.SearchOutputs(ctx, "foo_bar", 10)
	if err != nil {
		t.Fatalf("SearchOutputs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "under-match" {
		t.Errorf("expected under-match, got %s", results[0].ID)
	}
}

// TestSearchOutputs_PorterStemmer_MatchesRun verifies that searching "run"
// matches an output containing "running" via FTS5 porter stemmer tokenizer.
func TestSearchOutputs_PorterStemmer_MatchesRun(t *testing.T) {
	s, err := NewSQLiteStore(config.StoreConfig{Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	output := ToolOutput{
		ID:        "runner-id",
		ToolName:  "shell",
		Command:   "go test",
		Content:   "running all tests successfully",
		Timestamp: time.Now().UTC(),
	}
	if err := s.IndexOutput(ctx, output); err != nil {
		t.Fatalf("IndexOutput: %v", err)
	}

	// "run" should match "running" via FTS5 porter stemmer.
	// BuildFTSQuery("run") returns a non-empty FTS query, so the FTS5 path is used.
	results, err := s.SearchOutputs(ctx, "run", 10)
	if err != nil {
		t.Fatalf("SearchOutputs: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'run' matching 'running', got %d", len(results))
	}
	if results[0].ID != "runner-id" {
		t.Errorf("expected runner-id, got %s", results[0].ID)
	}
}
