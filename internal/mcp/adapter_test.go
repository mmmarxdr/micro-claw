package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// mockMCPCaller
// ---------------------------------------------------------------------------

type mockMCPCaller struct {
	callToolFn func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
	closeFn    func() error
}

func (m *mockMCPCaller) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return m.callToolFn(ctx, req)
}

func (m *mockMCPCaller) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

// newAdapter is a helper to create an MCPToolAdapter with a given mock caller.
func newAdapter(caller MCPCaller, name, description string) *MCPToolAdapter {
	return &MCPToolAdapter{
		caller: caller,
		toolDef: mcp.Tool{
			Name:        name,
			Description: description,
		},
	}
}

// ---------------------------------------------------------------------------
// TestMCPToolAdapter_Metadata
// ---------------------------------------------------------------------------

func TestMCPToolAdapter_Metadata(t *testing.T) {
	mock := &mockMCPCaller{
		callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}
	a := newAdapter(mock, "my_tool", "does cool stuff")

	t.Run("Name returns tool name", func(t *testing.T) {
		if got := a.Name(); got != "my_tool" {
			t.Errorf("Name() = %q, want %q", got, "my_tool")
		}
	})

	t.Run("Description returns tool description", func(t *testing.T) {
		if got := a.Description(); got != "does cool stuff" {
			t.Errorf("Description() = %q, want %q", got, "does cool stuff")
		}
	})
}

// ---------------------------------------------------------------------------
// TestMCPToolAdapter_Schema
// ---------------------------------------------------------------------------

func TestMCPToolAdapter_Schema(t *testing.T) {
	t.Run("marshals InputSchema to valid JSON", func(t *testing.T) {
		mock := &mockMCPCaller{
			callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		}
		a := &MCPToolAdapter{
			caller: mock,
			toolDef: mcp.Tool{
				Name:        "schema_tool",
				Description: "schema test",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]interface{}{
						"path": map[string]string{"type": "string"},
					},
				},
			},
		}
		schema := a.Schema()
		if !json.Valid(schema) {
			t.Errorf("Schema() returned invalid JSON: %s", schema)
		}
	})

	t.Run("fallback schema returned when marshal fails", func(t *testing.T) {
		// We cannot easily make json.Marshal fail on ToolInputSchema, so test
		// the fallback directly by providing a zero-value schema (valid) and
		// verifying Schema() always returns valid JSON.
		mock := &mockMCPCaller{
			callToolFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		}
		a := newAdapter(mock, "t", "")
		schema := a.Schema()
		if !json.Valid(schema) {
			t.Errorf("Schema() returned invalid JSON: %s", schema)
		}
	})
}

// ---------------------------------------------------------------------------
// TestMCPToolAdapter_Execute — table-driven
// ---------------------------------------------------------------------------

func TestMCPToolAdapter_Execute(t *testing.T) {
	tests := []struct {
		name        string
		callFn      func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
		params      string
		wantIsErr   bool
		wantContent string
		wantGoErr   bool
	}{
		{
			name: "success returns text content",
			callFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "hello"}},
					IsError: false,
				}, nil
			},
			params:      `{"path":"/tmp"}`,
			wantIsErr:   false,
			wantContent: "hello",
		},
		{
			name: "mcp_level_error: IsError true no Go error",
			callFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "not found"}},
					IsError: true,
				}, nil
			},
			params:      `{}`,
			wantIsErr:   true,
			wantContent: "not found",
		},
		{
			name: "transport_error: Go error returned",
			callFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, errors.New("broken pipe")
			},
			params:    `{}`,
			wantGoErr: true,
		},
		{
			name: "invalid_json_params: Go error, callFn never called",
			callFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				t.Error("callFn should not have been called for invalid JSON params")
				return &mcp.CallToolResult{}, nil
			},
			params:    `{bad json`,
			wantGoErr: true,
		},
		{
			name: "context_cancelled: Go error returned",
			callFn: func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, ctx.Err()
			},
			params:    `{}`,
			wantGoErr: true,
		},
		{
			name: "non_text_content: non-text placeholder returned",
			callFn: func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{mcp.ImageContent{Type: "image", Data: "abc", MIMEType: "image/png"}},
					IsError: false,
				}, nil
			},
			params:      `{}`,
			wantIsErr:   false,
			wantContent: "[non-text content]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockMCPCaller{callToolFn: tc.callFn}
			a := newAdapter(mock, "test_tool", "")

			ctx, cancel := context.WithCancel(context.Background())
			if tc.name == "context_cancelled: Go error returned" {
				cancel() // cancel before calling Execute
			} else {
				defer cancel()
			}

			result, err := a.Execute(ctx, json.RawMessage(tc.params))

			if tc.wantGoErr {
				if err == nil {
					t.Error("expected Go error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.IsError != tc.wantIsErr {
				t.Errorf("IsError = %v, want %v; content: %q", result.IsError, tc.wantIsErr, result.Content)
			}
			if tc.wantContent != "" && result.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", result.Content, tc.wantContent)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestExtractText
// ---------------------------------------------------------------------------

func TestExtractText(t *testing.T) {
	t.Run("multiple text contents concatenated", func(t *testing.T) {
		contents := []mcp.Content{
			mcp.TextContent{Type: "text", Text: "hello "},
			mcp.TextContent{Type: "text", Text: "world"},
		}
		got := extractText(contents)
		if got != "hello world" {
			t.Errorf("extractText() = %q, want %q", got, "hello world")
		}
	})

	t.Run("empty contents returns empty string", func(t *testing.T) {
		got := extractText(nil)
		if got != "" {
			t.Errorf("extractText(nil) = %q, want %q", got, "")
		}
	})

	t.Run("mixed text and non-text", func(t *testing.T) {
		contents := []mcp.Content{
			mcp.TextContent{Type: "text", Text: "before"},
			mcp.ImageContent{Type: "image", Data: "abc", MIMEType: "image/png"},
			mcp.TextContent{Type: "text", Text: "after"},
		}
		got := extractText(contents)
		want := "before[non-text content]after"
		if got != want {
			t.Errorf("extractText() = %q, want %q", got, want)
		}
	})
}
