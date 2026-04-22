package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"daimon/internal/rag/metrics"
	"daimon/internal/tool"
)

// RAGToolDeps holds the dependencies for the RAG tool set.
type RAGToolDeps struct {
	Worker        *DocIngestionWorker
	Store         DocumentStore
	EmbedFn       func(ctx context.Context, text string) ([]float32, error) // nil = FTS-only search
	HypothesisFn  func(ctx context.Context, query string) (string, error)   // nil = HyDE disabled
	HydeConf      HydeSearchConfig                                           // zero = HyDE disabled
	RetrievalConf RetrievalSearchConfig                                      // zero = default limit
	Recorder      metrics.Recorder                                           // nil-safe
}

// ToolResult is an alias to tool.ToolResult for ergonomic use in tests.
type ToolResult = tool.ToolResult

// Tool is an alias to tool.Tool.
type Tool = tool.Tool

// BuildRAGTools constructs the index_doc and search_docs tools.
func BuildRAGTools(deps RAGToolDeps) []tool.Tool {
	return []tool.Tool{
		&indexDocTool{deps: deps},
		&searchDocsTool{deps: deps},
	}
}

// ─── index_doc ───────────────────────────────────────────────────────────────

type indexDocTool struct {
	deps RAGToolDeps
}

func (t *indexDocTool) Name() string { return "index_doc" }

func (t *indexDocTool) Description() string {
	return "Index a document into the RAG knowledge base for later retrieval. " +
		"Provide either inline text or a sha256 reference to a media store blob."
}

func (t *indexDocTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["title"],
  "properties": {
    "text": {
      "type": "string",
      "description": "Inline document text to index"
    },
    "sha256": {
      "type": "string",
      "description": "SHA-256 reference to a blob in the media store"
    },
    "title": {
      "type": "string",
      "description": "Human-readable title for the document"
    },
    "namespace": {
      "type": "string",
      "description": "Namespace for scoping (default: global)",
      "default": "global"
    },
    "mime": {
      "type": "string",
      "description": "MIME type of the content (default: text/plain)"
    }
  }
}`)
}

type indexDocParams struct {
	Text      string `json:"text,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Title     string `json:"title"`
	Namespace string `json:"namespace,omitempty"`
	MIME      string `json:"mime,omitempty"`
}

func (t *indexDocTool) Execute(_ context.Context, params json.RawMessage) (tool.ToolResult, error) {
	var input indexDocParams
	if err := json.Unmarshal(params, &input); err != nil {
		return tool.ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.Title) == "" {
		return tool.ToolResult{IsError: true, Content: "title cannot be empty"}, nil
	}
	if strings.TrimSpace(input.Text) == "" && strings.TrimSpace(input.SHA256) == "" {
		return tool.ToolResult{IsError: true, Content: "either text or sha256 must be provided"}, nil
	}

	ns := input.Namespace
	if ns == "" {
		ns = "global"
	}
	mime := input.MIME
	if mime == "" {
		mime = "text/plain"
	}

	job := IngestionJob{
		DocID:     fmt.Sprintf("doc-%s", sanitizeID(input.Title)),
		Namespace: ns,
		Title:     input.Title,
		Content:   input.Text,
		SHA256:    input.SHA256,
		MIME:      mime,
	}

	if t.deps.Worker != nil {
		t.deps.Worker.Enqueue(job)
	}

	return tool.ToolResult{Content: fmt.Sprintf("Document queued for indexing: %s", input.Title)}, nil
}

// ─── search_docs ─────────────────────────────────────────────────────────────

type searchDocsTool struct {
	deps RAGToolDeps
}

func (t *searchDocsTool) Name() string { return "search_docs" }

func (t *searchDocsTool) Description() string {
	return "Search indexed documents for content relevant to the given query. " +
		"Uses full-text search with optional vector reranking when embeddings are available."
}

func (t *searchDocsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": {
      "type": "string",
      "description": "Search query"
    },
    "top_k": {
      "type": "integer",
      "description": "Maximum number of results to return (default: 5)",
      "minimum": 1,
      "maximum": 20
    },
    "namespace": {
      "type": "string",
      "description": "Namespace to search within (default: global)",
      "default": "global"
    }
  }
}`)
}

type searchDocsParams struct {
	Query     string `json:"query"`
	TopK      int    `json:"top_k,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

func (t *searchDocsTool) Execute(ctx context.Context, params json.RawMessage) (tool.ToolResult, error) {
	var input searchDocsParams
	if err := json.Unmarshal(params, &input); err != nil {
		return tool.ToolResult{IsError: true, Content: fmt.Sprintf("invalid parameters: %v", err)}, nil
	}

	if strings.TrimSpace(input.Query) == "" {
		return tool.ToolResult{IsError: true, Content: "query cannot be empty"}, nil
	}

	topK := input.TopK
	if topK <= 0 {
		topK = 5
	}
	if topK > 20 {
		topK = 20
	}

	// Resolve hypothesis timeout — default 10s when not configured.
	hydeConf := t.deps.HydeConf
	if hydeConf.HypothesisTimeout <= 0 {
		hydeConf.HypothesisTimeout = 10 * time.Second
	}

	// RetrievalConf.Limit is overridden by the tool's topK.
	retrieval := t.deps.RetrievalConf
	retrieval.Limit = topK

	hydeDeps := HydeSearchDeps{
		Store:         t.deps.Store,
		HypothesisFn:  t.deps.HypothesisFn,
		EmbedFn:       t.deps.EmbedFn,
		HydeConf:      hydeConf,
		RetrievalConf: retrieval,
		Recorder:      t.deps.Recorder,
	}

	results, err := PerformHydeSearch(ctx, input.Query, hydeDeps)
	if err != nil {
		return tool.ToolResult{IsError: true, Content: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(results) == 0 {
		return tool.ToolResult{Content: "No documents found."}, nil
	}

	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. [%s] (score: %.3f)\n%s\n\n",
			i+1, r.DocTitle, r.Score, r.Chunk.Content)
	}

	return tool.ToolResult{Content: strings.TrimRight(sb.String(), "\n")}, nil
}

// sanitizeID converts a title to a simple lowercase identifier.
func sanitizeID(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	result := sb.String()
	// Collapse multiple dashes.
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}
