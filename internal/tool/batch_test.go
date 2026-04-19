package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"daimon/internal/store"
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

// mockOutputStoreError always returns an error from IndexOutput.
type mockOutputStoreError struct {
	indexedOutputs []store.ToolOutput
	indexErr       error
}

func (m *mockOutputStoreError) IndexOutput(ctx context.Context, output store.ToolOutput) error {
	m.indexedOutputs = append(m.indexedOutputs, output)
	return m.indexErr
}

func (m *mockOutputStoreError) SearchOutputs(ctx context.Context, query string, limit int) ([]store.ToolOutput, error) {
	return nil, nil
}

// TestBatchExec_IndexError_Logged verifies that when IndexOutput fails on the
// success path, the error is logged at Warn level and execution continues
// (function does NOT early-return).
func TestBatchExec_IndexError_Logged(t *testing.T) {
	mockStore := &mockOutputStoreError{
		indexErr: errors.New("store unavailable"),
	}

	// Capture slog output.
	var logBuf bytes.Buffer
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(handler)
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	tool := NewBatchExecTool(mockStore, BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	params := json.RawMessage(`{"commands": ["echo cmd1", "echo cmd2"]}`)
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	// Both commands should have been attempted (no early return on index error).
	if len(mockStore.indexedOutputs) != 2 {
		t.Errorf("Expected 2 index attempts, got %d — function may have early-returned", len(mockStore.indexedOutputs))
	}

	// Result should still report success for the commands themselves.
	if result.IsError {
		t.Errorf("Expected IsError=false (commands succeeded despite index error), got true. Content: %s", result.Content)
	}

	// slog.Warn should have been called at least once.
	logOutput := logBuf.String()
	if logOutput == "" {
		t.Error("Expected at least one Warn log entry for index error, got none")
	}
	if !bytes.Contains([]byte(logOutput), []byte("batch_exec: failed to index output")) {
		t.Errorf("Expected log message 'batch_exec: failed to index output', got: %s", logOutput)
	}
}

// TestBatchExec_UniqueIDs verifies that all indexed ToolOutput entries have
// unique IDs that are valid UUIDs.
func TestBatchExec_UniqueIDs(t *testing.T) {
	mockStore := &mockOutputStoreForBatch{}

	tool := NewBatchExecTool(mockStore, BatchExecToolConfig{
		MaxOutputBytes: 1024 * 1024,
		Timeout:        30 * time.Second,
	})

	const commandCount = 5
	params := json.RawMessage(`{
		"commands": ["echo a", "echo b", "echo c", "echo d", "echo e"]
	}`)

	_, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if len(mockStore.indexedOutputs) != commandCount {
		t.Fatalf("Expected %d indexed outputs, got %d", commandCount, len(mockStore.indexedOutputs))
	}

	seen := make(map[string]struct{}, commandCount)
	for _, out := range mockStore.indexedOutputs {
		// Must be a valid UUID.
		if _, parseErr := uuid.Parse(out.ID); parseErr != nil {
			t.Errorf("ID %q is not a valid UUID: %v", out.ID, parseErr)
		}
		// Must be unique.
		if _, dup := seen[out.ID]; dup {
			t.Errorf("Duplicate ID found: %q", out.ID)
		}
		seen[out.ID] = struct{}{}
	}
}

// TestTrimPreviewRunes exercises the trimPreviewRunes helper with table-driven cases
// covering empty, short, exact-100, over-100-ascii, and over-100-multibyte inputs.
func TestTrimPreviewRunes(t *testing.T) {
	// A single emoji is 1 rune but multiple bytes.
	emoji := "😀" // 4 bytes, 1 rune
	// A string of exactly 100 runes.
	exact100 := string(make([]rune, 100))
	for i := range []rune(exact100) {
		exact100 = exact100[:i] + "a" + exact100[i+1:]
	}
	// Build exact100 properly.
	r100 := make([]rune, 100)
	for i := range r100 {
		r100[i] = 'x'
	}
	exact100 = string(r100)

	// 101 ASCII runes.
	r101ASCII := make([]rune, 101)
	for i := range r101ASCII {
		r101ASCII[i] = 'y'
	}
	s101ASCII := string(r101ASCII)

	// 101 multibyte runes (emoji).
	r101Multi := make([]rune, 101)
	for i := range r101Multi {
		r101Multi[i] = '😀'
	}
	s101Multi := string(r101Multi)

	cases := []struct {
		name        string
		input       string
		maxRunes    int
		wantTrimmed bool   // whether "..." suffix expected
		wantRunes   int    // expected rune count of result (excluding "...")
		wantValid   bool   // utf8.ValidString
	}{
		{
			name: "empty",
			input: "", maxRunes: 100,
			wantTrimmed: false, wantRunes: 0, wantValid: true,
		},
		{
			name: "short",
			input: "hello", maxRunes: 100,
			wantTrimmed: false, wantRunes: 5, wantValid: true,
		},
		{
			name: "exact-100",
			input: exact100, maxRunes: 100,
			wantTrimmed: false, wantRunes: 100, wantValid: true,
		},
		{
			name: "101-ascii",
			input: s101ASCII, maxRunes: 100,
			wantTrimmed: true, wantRunes: 100, wantValid: true,
		},
		{
			name: "101-multibyte",
			input: s101Multi, maxRunes: 100,
			wantTrimmed: true, wantRunes: 100, wantValid: true,
		},
		{
			name: "single-emoji",
			input: emoji, maxRunes: 100,
			wantTrimmed: false, wantRunes: 1, wantValid: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimPreviewRunes(tc.input, tc.maxRunes)

			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8: %q", got)
			}

			gotRunes := []rune(got)

			if tc.wantTrimmed {
				if len(got) < 3 || got[len(got)-3:] != "..." {
					t.Errorf("expected '...' suffix, got: %q", got)
				}
				// Body runes (excluding the 3-byte "...").
				bodyRunes := []rune(got[:len(got)-3])
				if len(bodyRunes) != tc.wantRunes {
					t.Errorf("expected %d body runes before '...', got %d", tc.wantRunes, len(bodyRunes))
				}
			} else {
				if len(gotRunes) != tc.wantRunes {
					t.Errorf("expected %d runes, got %d", tc.wantRunes, len(gotRunes))
				}
			}
		})
	}
}
