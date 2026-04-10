package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"microagent/internal/store"
)

// TestSearchOutputTool_Execute tests the SearchOutputTool searches outputs correctly.
func TestSearchOutputTool_Execute(t *testing.T) {
	mockStore := &mockSearchOutputStore{}

	tool := NewSearchOutputTool(mockStore)

	// First, index some outputs
	mockStore.indexedOutputs = []store.ToolOutput{
		{
			ID:        "output-1",
			ToolName:  "shell",
			Command:   "echo hello world",
			Content:   "hello world output",
			Truncated: false,
			ExitCode:  0,
			Timestamp: time.Now(),
		},
		{
			ID:        "output-2",
			ToolName:  "http",
			Command:   "GET /api/data",
			Content:   "some JSON data response",
			Truncated: false,
			ExitCode:  200,
			Timestamp: time.Now(),
		},
		{
			ID:        "output-3",
			ToolName:  "shell",
			Command:   "ls -la",
			Content:   "total 4096 drwxr-xr-x  12 root root 4096 Apr  9 12:00 .",
			Truncated: false,
			ExitCode:  0,
			Timestamp: time.Now(),
		},
	}

	params := json.RawMessage(`{
		"query": "hello",
		"limit": 10
	}`)

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.Content)
	}

	// Verify search was called with correct query
	if mockStore.lastQuery != "hello" {
		t.Errorf("Expected query 'hello', got %s", mockStore.lastQuery)
	}

	// Verify result count in meta
	if result.Meta["result_count"] != "1" {
		t.Errorf("Expected result_count=1, got %s", result.Meta["result_count"])
	}
}

// TestSearchOutputTool_NoResults tests when no outputs match the query.
func TestSearchOutputTool_NoResults(t *testing.T) {
	mockStore := &mockSearchOutputStore{
		searchResults: []store.ToolOutput{},
	}

	tool := NewSearchOutputTool(mockStore)

	params := json.RawMessage(`{
		"query": "nonexistent",
		"limit": 10
	}`)

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.Content)
	}

	if result.Content != "No matching tool outputs found." {
		t.Errorf("Expected 'No matching tool outputs found.', got: %s", result.Content)
	}
}

// TestSearchOutputTool_Schema tests that SearchOutputTool returns valid JSON schema.
func TestSearchOutputTool_Schema(t *testing.T) {
	tool := NewSearchOutputTool(&mockSearchOutputStore{})

	schema := tool.Schema()
	if len(schema) == 0 {
		t.Error("Schema should not be empty")
	}

	// Verify it's valid JSON
	var schemaObj map[string]interface{}
	if err := json.Unmarshal(schema, &schemaObj); err != nil {
		t.Errorf("Schema is not valid JSON: %v", err)
	}
}

// mockSearchOutputStore is a mock OutputStore for testing SearchOutputTool.
type mockSearchOutputStore struct {
	indexedOutputs []store.ToolOutput
	searchResults  []store.ToolOutput
	lastQuery      string
	lastLimit      int
}

func (m *mockSearchOutputStore) IndexOutput(ctx context.Context, output store.ToolOutput) error {
	m.indexedOutputs = append(m.indexedOutputs, output)
	return nil
}

func (m *mockSearchOutputStore) SearchOutputs(ctx context.Context, query string, limit int) ([]store.ToolOutput, error) {
	m.lastQuery = query
	m.lastLimit = limit
	if m.searchResults == nil {
		// Default: return all indexed outputs that contain the query
		var results []store.ToolOutput
		lowerQuery := strings.ToLower(query)
		for _, o := range m.indexedOutputs {
			if strings.Contains(strings.ToLower(o.Content), lowerQuery) ||
				strings.Contains(strings.ToLower(o.ToolName), lowerQuery) ||
				strings.Contains(strings.ToLower(o.Command), lowerQuery) {
				results = append(results, o)
				if limit > 0 && len(results) >= limit {
					break
				}
			}
		}
		return results, nil
	}
	return m.searchResults, nil
}
