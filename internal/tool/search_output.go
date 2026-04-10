package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"microagent/internal/store"
)

// SearchOutputTool searches indexed tool outputs via FTS5.
type SearchOutputTool struct {
	store store.OutputStore
}

// NewSearchOutputTool creates a new SearchOutputTool.
func NewSearchOutputTool(store store.OutputStore) *SearchOutputTool {
	return &SearchOutputTool{store: store}
}

// Name returns the tool name.
func (t *SearchOutputTool) Name() string {
	return "search_output"
}

// Description returns the tool description.
func (t *SearchOutputTool) Description() string {
	return "Search previously executed tool outputs using full-text search. Use this to find command outputs by keywords, tool names, or command patterns."
}

// Schema returns the JSON schema for the tool parameters.
func (t *SearchOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{
	  "type": "object",
	  "properties": {
		"query": {
		  "type": "string",
		  "description": "Search query (keywords to search for in tool outputs)"
		},
		"limit": {
		  "type": "integer",
		  "default": 10,
		  "description": "Maximum number of results to return"
		}
	  },
	  "required": ["query"]
	}`)
}

type searchOutputParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Execute searches indexed tool outputs and returns matching results.
func (t *SearchOutputTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var input searchOutputParams
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("parsing params: %v", err)}, nil
	}

	if input.Query == "" {
		return ToolResult{IsError: true, Content: "query cannot be empty"}, nil
	}

	if input.Limit <= 0 {
		input.Limit = 10
	}

	results, err := t.store.SearchOutputs(ctx, input.Query, input.Limit)
	if err != nil {
		return ToolResult{IsError: true, Content: fmt.Sprintf("search failed: %v", err)}, nil
	}

	if len(results) == 0 {
		return ToolResult{
			Content: "No matching tool outputs found.",
			Meta:    map[string]string{"result_count": "0"},
		}, nil
	}

	// Build result summary
	var lines []string
	for i, r := range results {
		preview := r.Content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		// Replace newlines with spaces for summary
		preview = strings.ReplaceAll(preview, "\n", " ")
		lines = append(lines, fmt.Sprintf("[%d] %s: %s (exit=%d)", i+1, r.ToolName, preview, r.ExitCode))
	}

	content := fmt.Sprintf("Found %d matching outputs:\n\n%s", len(results), strings.Join(lines, "\n"))

	meta := map[string]string{
		"result_count": fmt.Sprintf("%d", len(results)),
		"query":        input.Query,
	}

	return ToolResult{
		Content: content,
		Meta:    meta,
	}, nil
}
