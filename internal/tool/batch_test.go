package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"microagent/internal/store"
)

// TestBatchExecTool_Execute tests the BatchExecTool executes commands sequentially
// and indexes outputs.
func TestBatchExecTool_Execute(t *testing.T) {
	// Create a mock OutputStore
	mockStore := &mockOutputStoreForBatch{}

	tool := NewBatchExecTool(mockStore, BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	params := json.RawMessage(`{
		"commands": ["echo hello", "echo world"],
		"stop_on_error": false
	}`)

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.Content)
	}

	// Verify at least one output was indexed
	if len(mockStore.indexedOutputs) != 2 {
		t.Errorf("Expected 2 outputs indexed, got %d", len(mockStore.indexedOutputs))
	}

	// Verify summary contains both outputs
	if result.Meta["command_count"] != "2" {
		t.Errorf("Expected command_count=2, got %s", result.Meta["command_count"])
	}
}

// TestBatchExecTool_StopOnError tests that BatchExecTool stops on error when configured.
func TestBatchExecTool_StopOnError(t *testing.T) {
	mockStore := &mockOutputStoreForBatch{}

	tool := NewBatchExecTool(mockStore, BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	// First command succeeds, second fails (nonexistent command), third should not run
	params := json.RawMessage(`{
		"commands": ["echo success", "nonexistentcmd123", "echo shouldnotrun"],
		"stop_on_error": true
	}`)

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Should have error in result due to failed command
	if !result.IsError {
		t.Errorf("Expected IsError=true after failed command with stop_on_error, got false. Content: %s", result.Content)
	}

	// Only first two commands should have been attempted
	if len(mockStore.indexedOutputs) != 2 {
		t.Errorf("Expected 2 outputs indexed (including error), got %d", len(mockStore.indexedOutputs))
	}
}

// TestBatchExecTool_Schema tests that BatchExecTool returns valid JSON schema.
func TestBatchExecTool_Schema(t *testing.T) {
	mockStore := &mockOutputStoreForBatch{}
	tool := NewBatchExecTool(mockStore, BatchExecToolConfig{})

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

// mockOutputStoreForBatch is a mock OutputStore for testing BatchExecTool.
type mockOutputStoreForBatch struct {
	indexedOutputs []store.ToolOutput
}

func (m *mockOutputStoreForBatch) IndexOutput(ctx context.Context, output store.ToolOutput) error {
	m.indexedOutputs = append(m.indexedOutputs, output)
	return nil
}

func (m *mockOutputStoreForBatch) SearchOutputs(ctx context.Context, query string, limit int) ([]store.ToolOutput, error) {
	return nil, nil
}
