package agent

// T6.1 tests: buildRAGSection and buildSystemPrompt with RAG results.

import (
	"strings"
	"testing"

	"microagent/internal/config"
	"microagent/internal/rag"
	"microagent/internal/store"
)

// makeSearchResult builds a SearchResult with the given title, chunkIndex, and content.
func makeSearchResult(title string, chunkIndex int, chunkContent string) rag.SearchResult {
	return rag.SearchResult{
		DocTitle: title,
		Score:    0.9,
		Chunk: rag.DocumentChunk{
			ID:      "chunk-id",
			DocID:   "doc-id",
			Index:   chunkIndex,
			Content: chunkContent,
		},
	}
}

// minimalAgent returns a bare Agent with minimal config, suitable for testing
// buildSystemPrompt / buildRAGSection.
func minimalAgent() *Agent {
	return &Agent{
		config: config.AgentConfig{
			MaxContextTokens: 10000,
		},
	}
}

// ---------------------------------------------------------------------------
// buildRAGSection tests
// ---------------------------------------------------------------------------

func TestBuildRAGSection_EmptyResults_ReturnsEmpty(t *testing.T) {
	got := buildRAGSection(nil, 10000)
	if got != "" {
		t.Errorf("expected empty string for nil results, got %q", got)
	}

	got = buildRAGSection([]rag.SearchResult{}, 10000)
	if got != "" {
		t.Errorf("expected empty string for empty results, got %q", got)
	}
}

func TestBuildRAGSection_CorrectFormat(t *testing.T) {
	results := []rag.SearchResult{
		makeSearchResult("Go Tutorial", 0, "Go is a compiled language."),
		makeSearchResult("Python Guide", 2, "Python is interpreted."),
	}

	got := buildRAGSection(results, 10000)

	if !strings.Contains(got, "## Relevant Documents:") {
		t.Errorf("missing '## Relevant Documents:' header, got:\n%s", got)
	}
	if !strings.Contains(got, "### Go Tutorial (chunk 1)") {
		t.Errorf("missing '### Go Tutorial (chunk 1)', got:\n%s", got)
	}
	if !strings.Contains(got, "Go is a compiled language.") {
		t.Errorf("missing chunk content, got:\n%s", got)
	}
	if !strings.Contains(got, "### Python Guide (chunk 3)") {
		t.Errorf("missing '### Python Guide (chunk 3)', got:\n%s", got)
	}
	if !strings.Contains(got, "Python is interpreted.") {
		t.Errorf("missing second chunk content, got:\n%s", got)
	}
}

func TestBuildRAGSection_RespectsTokenBudget(t *testing.T) {
	// Each chunk has ~200 chars (~56 tokens including header). The section header is ~6 tokens.
	// Header(6) + first_entry(56) = 62 tokens.
	// Budget 70 fits first entry but not second (62+56=118 > 70).
	longContent := strings.Repeat("a", 200)
	results := []rag.SearchResult{
		makeSearchResult("Doc One", 0, longContent),
		makeSearchResult("Doc Two", 1, longContent),
		makeSearchResult("Doc Three", 2, longContent),
	}

	got := buildRAGSection(results, 70)

	if !strings.Contains(got, "Doc One") {
		t.Errorf("expected Doc One to be included, got:\n%s", got)
	}
	if strings.Contains(got, "Doc Three") {
		t.Errorf("expected Doc Three to be excluded by token budget, got:\n%s", got)
	}
}

func TestBuildRAGSection_ZeroMaxTokens_IncludesAll(t *testing.T) {
	// maxTokens == 0 means no budget cap — all chunks included.
	results := make([]rag.SearchResult, 10)
	for i := range results {
		results[i] = makeSearchResult("Doc", i, strings.Repeat("word ", 20))
	}

	got := buildRAGSection(results, 0)
	if strings.Count(got, "### Doc") != 10 {
		t.Errorf("expected 10 chunks with no budget cap, got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// buildSystemPrompt with RAG results
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_WithRAGResults_BothSectionsPresent(t *testing.T) {
	ag := minimalAgent()

	memories := []store.MemoryEntry{
		{Content: "Remember this fact."},
	}

	ragResults := []rag.SearchResult{
		makeSearchResult("API Docs", 0, "The API accepts JSON."),
	}

	prompt := ag.buildSystemPrompt(memories, ragResults)

	if !strings.Contains(prompt, "## Relevant Context:") {
		t.Errorf("expected memory section '## Relevant Context:', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Remember this fact.") {
		t.Errorf("expected memory content in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## Relevant Documents:") {
		t.Errorf("expected RAG section '## Relevant Documents:', got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "The API accepts JSON.") {
		t.Errorf("expected RAG content in prompt, got:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_WithNilRAGResults_NoRAGSection(t *testing.T) {
	ag := minimalAgent()
	prompt := ag.buildSystemPrompt(nil, nil)
	if strings.Contains(prompt, "## Relevant Documents:") {
		t.Errorf("expected no RAG section for nil results, got:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_WithEmptyRAGResults_NoRAGSection(t *testing.T) {
	ag := minimalAgent()
	prompt := ag.buildSystemPrompt(nil, []rag.SearchResult{})
	if strings.Contains(prompt, "## Relevant Documents:") {
		t.Errorf("expected no RAG section for empty results, got:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_RAGSection_AfterMemorySection(t *testing.T) {
	ag := minimalAgent()
	memories := []store.MemoryEntry{{Content: "memory item"}}
	ragResults := []rag.SearchResult{makeSearchResult("MyDoc", 0, "rag content")}

	prompt := ag.buildSystemPrompt(memories, ragResults)

	memIdx := strings.Index(prompt, "## Relevant Context:")
	ragIdx := strings.Index(prompt, "## Relevant Documents:")
	if memIdx == -1 || ragIdx == -1 {
		t.Fatalf("expected both sections: memIdx=%d ragIdx=%d", memIdx, ragIdx)
	}
	if ragIdx <= memIdx {
		t.Errorf("expected RAG section after memory section: memIdx=%d ragIdx=%d", memIdx, ragIdx)
	}
}
