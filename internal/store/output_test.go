package store

import (
	"context"
	"testing"
	"time"
)

func TestToolOutputStruct(t *testing.T) {
	now := time.Now()
	output := ToolOutput{
		ID:        "test-id-123",
		ToolName:  "shell",
		Command:   "echo hello",
		Content:   "hello\n",
		Truncated: false,
		ExitCode:  0,
		Timestamp: now,
	}

	if output.ID != "test-id-123" {
		t.Errorf("ToolOutput.ID = %q, want %q", output.ID, "test-id-123")
	}
	if output.ToolName != "shell" {
		t.Errorf("ToolOutput.ToolName = %q, want %q", output.ToolName, "shell")
	}
	if output.Command != "echo hello" {
		t.Errorf("ToolOutput.Command = %q, want %q", output.Command, "echo hello")
	}
	if output.Content != "hello\n" {
		t.Errorf("ToolOutput.Content = %q, want %q", output.Content, "hello\n")
	}
	if output.Truncated != false {
		t.Errorf("ToolOutput.Truncated = %v, want false", output.Truncated)
	}
	if output.ExitCode != 0 {
		t.Errorf("ToolOutput.ExitCode = %d, want 0", output.ExitCode)
	}
	if !output.Timestamp.Equal(now) {
		t.Errorf("ToolOutput.Timestamp = %v, want %v", output.Timestamp, now)
	}
}

func TestOutputStoreInterface(t *testing.T) {
	// This test verifies the interface exists and has the expected methods
	// We'll create a mock that implements the interface
	var store OutputStore = &mockOutputStore{}

	ctx := context.Background()
	output := ToolOutput{
		ID:        "test-id",
		ToolName:  "test",
		Content:   "test content",
		Timestamp: time.Now(),
	}

	// Test IndexOutput method exists
	err := store.IndexOutput(ctx, output)
	if err != nil {
		t.Errorf("IndexOutput returned error: %v", err)
	}

	// Test SearchOutputs method exists
	results, err := store.SearchOutputs(ctx, "test", 10)
	if err != nil {
		t.Errorf("SearchOutputs returned error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("SearchOutputs returned %d results, want 1", len(results))
	}
}

// mockOutputStore is a minimal implementation for testing the interface
type mockOutputStore struct{}

func (m *mockOutputStore) IndexOutput(ctx context.Context, output ToolOutput) error {
	return nil
}

func (m *mockOutputStore) SearchOutputs(ctx context.Context, query string, limit int) ([]ToolOutput, error) {
	return []ToolOutput{
		{
			ID:        "mock-id",
			ToolName:  "mock",
			Content:   "mock content",
			Timestamp: time.Now(),
		},
	}, nil
}
