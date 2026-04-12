package provider

import (
	"encoding/json"
	"os"
	"testing"

	"microagent/internal/content"
)

// TestChatMessage_UnmarshalJSON_LegacyString verifies that a plain JSON string
// content field deserialises as a single text block.
func TestChatMessage_UnmarshalJSON_LegacyString(t *testing.T) {
	input := `{"role":"user","content":"hello, world"}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Content) != 1 {
		t.Fatalf("expected 1 block, got %d", len(m.Content))
	}
	if m.Content[0].Type != content.BlockText {
		t.Errorf("expected BlockText, got %q", m.Content[0].Type)
	}
	if m.Content[0].Text != "hello, world" {
		t.Errorf("expected %q, got %q", "hello, world", m.Content[0].Text)
	}
	if m.Role != "user" {
		t.Errorf("expected role %q, got %q", "user", m.Role)
	}
}

// TestChatMessage_UnmarshalJSON_EmptyString verifies that an empty string content
// field produces nil Content (no blocks).
func TestChatMessage_UnmarshalJSON_EmptyString(t *testing.T) {
	input := `{"role":"assistant","content":""}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Content) != 0 {
		t.Errorf("expected empty/nil blocks for empty string content, got %v", m.Content)
	}
}

// TestChatMessage_UnmarshalJSON_TextOnlyArray verifies that an array of text
// blocks is flattened by joining with newlines.
func TestChatMessage_UnmarshalJSON_TextOnlyArray(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single text block",
			input:    `{"role":"user","content":[{"type":"text","text":"hello"}]}`,
			expected: "hello",
		},
		{
			name:     "two text blocks joined",
			input:    `{"role":"user","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]}`,
			expected: "part1\npart2",
		},
		{
			name:     "empty array",
			input:    `{"role":"user","content":[]}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m ChatMessage
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := m.Content.TextOnly()
			if got != tt.expected {
				t.Errorf("TextOnly() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestChatMessage_UnmarshalJSON_NonTextBlocks verifies that non-text blocks
// are preserved as typed ContentBlock values with the correct BlockType.
func TestChatMessage_UnmarshalJSON_NonTextBlocks(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantBlockCount int
		wantFirstType  content.BlockType
		wantHasMedia   bool
	}{
		{
			name:           "image only",
			input:          `{"role":"user","content":[{"type":"image","media_sha256":"abc","mime":"image/jpeg","size":1024}]}`,
			wantBlockCount: 1,
			wantFirstType:  content.BlockImage,
			wantHasMedia:   true,
		},
		{
			name:           "audio only",
			input:          `{"role":"user","content":[{"type":"audio","media_sha256":"def","mime":"audio/ogg","size":512}]}`,
			wantBlockCount: 1,
			wantFirstType:  content.BlockAudio,
			wantHasMedia:   true,
		},
		{
			name:           "document with filename",
			input:          `{"role":"user","content":[{"type":"document","media_sha256":"xyz","mime":"application/pdf","size":2048,"filename":"invoice.pdf"}]}`,
			wantBlockCount: 1,
			wantFirstType:  content.BlockDocument,
			wantHasMedia:   true,
		},
		{
			name:           "mixed text and image",
			input:          `{"role":"user","content":[{"type":"text","text":"caption"},{"type":"image","media_sha256":"abc","mime":"image/jpeg","size":1024}]}`,
			wantBlockCount: 2,
			wantFirstType:  content.BlockText,
			wantHasMedia:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m ChatMessage
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(m.Content) != tt.wantBlockCount {
				t.Errorf("block count = %d, want %d", len(m.Content), tt.wantBlockCount)
			}
			if len(m.Content) > 0 && m.Content[0].Type != tt.wantFirstType {
				t.Errorf("first block type = %q, want %q", m.Content[0].Type, tt.wantFirstType)
			}
			if m.Content.HasMedia() != tt.wantHasMedia {
				t.Errorf("HasMedia() = %v, want %v", m.Content.HasMedia(), tt.wantHasMedia)
			}
		})
	}
}

// TestChatMessage_UnmarshalJSON_PreservesToolFields verifies that tool_calls
// and tool_call_id are preserved correctly in both legacy and array paths.
func TestChatMessage_UnmarshalJSON_PreservesToolFields(t *testing.T) {
	input := `{"role":"tool","content":"result","tool_call_id":"call-123"}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ToolCallID != "call-123" {
		t.Errorf("expected tool_call_id %q, got %q", "call-123", m.ToolCallID)
	}
	if m.Content.TextOnly() != "result" {
		t.Errorf("expected content TextOnly()=%q, got %q", "result", m.Content.TextOnly())
	}
}

// TestChatMessage_UnmarshalJSON_MalformedJSON verifies that malformed JSON
// returns an error and does not silently produce empty content.
func TestChatMessage_UnmarshalJSON_MalformedJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "truncated object",
			input: `{"role":"user","content":`,
		},
		{
			name:  "content is a number",
			input: `{"role":"user","content":42}`,
		},
		{
			name:  "content is a bool",
			input: `{"role":"user","content":true}`,
		},
		{
			name:  "malformed array",
			input: `{"role":"user","content":[{"type":}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m ChatMessage
			if err := json.Unmarshal([]byte(tt.input), &m); err == nil {
				t.Errorf("expected error for input %q, got nil", tt.input)
			}
		})
	}
}

// TestChatMessage_UnmarshalJSON_NullContent verifies null content is treated
// as nil/empty Blocks without error.
func TestChatMessage_UnmarshalJSON_NullContent(t *testing.T) {
	input := `{"role":"user","content":null}`
	var m ChatMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Content) != 0 {
		t.Errorf("expected empty/nil blocks for null content, got %v", m.Content)
	}
}

// TestChatMessage_MarshalJSON_WritesArrayForm verifies that marshalling a ChatMessage
// produces the new array-of-blocks form (write-forward). Phase 1 replaces the
// legacy string form with the array form on write.
func TestChatMessage_MarshalJSON_WritesArrayForm(t *testing.T) {
	m := ChatMessage{Role: "user", Content: content.TextBlock("hello")}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	contentRaw, ok := raw["content"]
	if !ok {
		t.Fatal("content field missing from marshalled output")
	}

	// Phase 1: Content is now serialised as a JSON array ('['), not a string ('"').
	if len(contentRaw) == 0 || contentRaw[0] != '[' {
		t.Errorf("expected content to be marshalled as a JSON array, got: %s", contentRaw)
	}

	// Round-trip: re-unmarshal should give back the same TextOnly value.
	var m2 ChatMessage
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("round-trip unmarshal error: %v", err)
	}
	if m2.Content.TextOnly() != "hello" {
		t.Errorf("round-trip TextOnly() = %q, want %q", m2.Content.TextOnly(), "hello")
	}
}

// TestChatMessage_FixtureFile_LegacyLoad verifies that the checked-in legacy
// fixture file deserialises correctly. This file must never be deleted.
func TestChatMessage_FixtureFile_LegacyLoad(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy_chatmessage.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	var m ChatMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal fixture: %v", err)
	}
	if m.Content.TextOnly() != "hello from before multimodal" {
		t.Errorf("unexpected content TextOnly(): %q", m.Content.TextOnly())
	}
	if m.Role != "user" {
		t.Errorf("unexpected role: %q", m.Role)
	}
}

// TestChatMessage_FixtureFile_ForwardCompatNewShape is the migration canary.
// It loads the checked-in fixture that contains the future array-shape JSON and
// asserts it deserialises correctly into the expected Blocks structure.
// This fixture MUST stay in the repo through the entire change lifecycle.
func TestChatMessage_FixtureFile_ForwardCompatNewShape(t *testing.T) {
	data, err := os.ReadFile("testdata/forward_compat_new_shape.json")
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	var m ChatMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal fixture: %v", err)
	}
	// The fixture has: text "part one", image, text "part two" — 3 blocks total.
	if len(m.Content) != 3 {
		t.Fatalf("migration canary: expected 3 blocks, got %d", len(m.Content))
	}
	if m.Content[0].Type != content.BlockText || m.Content[0].Text != "part one" {
		t.Errorf("migration canary: block[0] = %+v, want text 'part one'", m.Content[0])
	}
	if m.Content[1].Type != content.BlockImage {
		t.Errorf("migration canary: block[1] type = %q, want 'image'", m.Content[1].Type)
	}
	if m.Content[2].Type != content.BlockText || m.Content[2].Text != "part two" {
		t.Errorf("migration canary: block[2] = %+v, want text 'part two'", m.Content[2])
	}
	if m.Role != "user" {
		t.Errorf("unexpected role: %q", m.Role)
	}
	// TextOnly should produce "part one\npart two" (image block is skipped).
	got := m.Content.TextOnly()
	if got != "part one\npart two" {
		t.Errorf("migration canary TextOnly() = %q, want %q", got, "part one\npart two")
	}
}
