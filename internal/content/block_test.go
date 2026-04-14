package content

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// TextBlock constructor
// ---------------------------------------------------------------------------

func TestTextBlock(t *testing.T) {
	bs := TextBlock("hello")
	if len(bs) != 1 {
		t.Fatalf("expected 1 block, got %d", len(bs))
	}
	if bs[0].Type != BlockText {
		t.Errorf("expected BlockText, got %q", bs[0].Type)
	}
	if bs[0].Text != "hello" {
		t.Errorf("expected %q, got %q", "hello", bs[0].Text)
	}
}

// ---------------------------------------------------------------------------
// TextOnly
// ---------------------------------------------------------------------------

func TestBlocks_TextOnly(t *testing.T) {
	tests := []struct {
		name     string
		blocks   Blocks
		expected string
	}{
		{name: "empty", blocks: nil, expected: ""},
		{name: "empty slice", blocks: Blocks{}, expected: ""},
		{name: "single text", blocks: TextBlock("hello"), expected: "hello"},
		{
			name: "two text blocks",
			blocks: Blocks{
				{Type: BlockText, Text: "part1"},
				{Type: BlockText, Text: "part2"},
			},
			expected: "part1\npart2",
		},
		{
			name: "mixed text and image",
			blocks: Blocks{
				{Type: BlockText, Text: "caption"},
				{Type: BlockImage, MediaSHA256: "abc", MIME: "image/jpeg"},
			},
			expected: "caption",
		},
		{
			name: "all media no text",
			blocks: Blocks{
				{Type: BlockImage, MediaSHA256: "abc"},
				{Type: BlockAudio, MediaSHA256: "def"},
			},
			expected: "",
		},
		{
			name: "text with empty text field skipped",
			blocks: Blocks{
				{Type: BlockText, Text: ""},
				{Type: BlockText, Text: "real"},
			},
			expected: "real",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.blocks.TextOnly()
			if got != tt.expected {
				t.Errorf("TextOnly() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HasMedia
// ---------------------------------------------------------------------------

func TestBlocks_HasMedia(t *testing.T) {
	tests := []struct {
		name     string
		blocks   Blocks
		expected bool
	}{
		{name: "nil", blocks: nil, expected: false},
		{name: "empty", blocks: Blocks{}, expected: false},
		{name: "text only", blocks: TextBlock("hello"), expected: false},
		{
			name:     "image only",
			blocks:   Blocks{{Type: BlockImage, MediaSHA256: "abc"}},
			expected: true,
		},
		{
			name:     "audio only",
			blocks:   Blocks{{Type: BlockAudio, MediaSHA256: "def"}},
			expected: true,
		},
		{
			name: "mixed text and image",
			blocks: Blocks{
				{Type: BlockText, Text: "hi"},
				{Type: BlockImage, MediaSHA256: "abc"},
			},
			expected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.blocks.HasMedia()
			if got != tt.expected {
				t.Errorf("HasMedia() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UnmarshalBlocks
// ---------------------------------------------------------------------------

func TestUnmarshalBlocks(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expectLen int
		expectErr bool
		// optional field checks on first block
		expectType BlockType
		expectText string
	}{
		{name: "nil raw", input: "", expectLen: 0},
		{name: "null raw", input: "null", expectLen: 0},
		{
			name:       "legacy string",
			input:      `"hello from legacy"`,
			expectLen:  1,
			expectType: BlockText,
			expectText: "hello from legacy",
		},
		{
			name:      "empty legacy string",
			input:     `""`,
			expectLen: 0,
		},
		{
			name:      "array single text block",
			input:     `[{"type":"text","text":"hi"}]`,
			expectLen: 1,
			expectType: BlockText,
			expectText: "hi",
		},
		{
			name:       "array image block",
			input:      `[{"type":"image","media_sha256":"abc","mime":"image/jpeg","size":1024}]`,
			expectLen:  1,
			expectType: BlockImage,
		},
		{
			name:      "empty array",
			input:     `[]`,
			expectLen: 0,
		},
		{
			name:      "number is error",
			input:     `42`,
			expectErr: true,
		},
		{
			name:      "bool is error",
			input:     `true`,
			expectErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := json.RawMessage(tt.input)
			bs, err := UnmarshalBlocks(raw)
			if tt.expectErr {
				if err == nil {
					t.Errorf("expected error, got nil (blocks: %v)", bs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(bs) != tt.expectLen {
				t.Fatalf("expected %d blocks, got %d", tt.expectLen, len(bs))
			}
			if tt.expectLen > 0 {
				if tt.expectType != "" && bs[0].Type != tt.expectType {
					t.Errorf("block[0].Type = %q, want %q", bs[0].Type, tt.expectType)
				}
				if tt.expectText != "" && bs[0].Text != tt.expectText {
					t.Errorf("block[0].Text = %q, want %q", bs[0].Text, tt.expectText)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContentBlock JSON round-trip
// ---------------------------------------------------------------------------

func TestContentBlock_RoundTrip_TextBlock(t *testing.T) {
	orig := ContentBlock{Type: BlockText, Text: "hello world"}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var got ContentBlock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.Type != orig.Type || got.Text != orig.Text {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
	// Media fields must be zero for text block.
	if got.MediaSHA256 != "" || got.MIME != "" || got.Size != 0 {
		t.Errorf("unexpected media fields on text block: %+v", got)
	}
}

func TestContentBlock_RoundTrip_ImageBlock(t *testing.T) {
	orig := ContentBlock{
		Type:        BlockImage,
		MediaSHA256: "deadbeef",
		MIME:        "image/jpeg",
		Size:        102400,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var got ContentBlock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if got.Type != orig.Type ||
		got.MediaSHA256 != orig.MediaSHA256 ||
		got.MIME != orig.MIME ||
		got.Size != orig.Size {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
	if got.Text != "" {
		t.Errorf("unexpected Text on image block: %q", got.Text)
	}
}

// ---------------------------------------------------------------------------
// FlattenBlocks (degrade.go)
// ---------------------------------------------------------------------------

func TestFlattenBlocks_PureText(t *testing.T) {
	bs := Blocks{
		{Type: BlockText, Text: "hello"},
		{Type: BlockText, Text: "world"},
	}
	got := FlattenBlocks(bs)
	if got != "hello\nworld" {
		t.Errorf("FlattenBlocks pure-text = %q", got)
	}
}

func TestFlattenBlocks_MixedTextImage(t *testing.T) {
	bs := Blocks{
		{Type: BlockText, Text: "caption"},
		{Type: BlockImage, MediaSHA256: "abc", MIME: "image/jpeg", Size: 1200000},
	}
	got := FlattenBlocks(bs)
	if got == "" {
		t.Fatal("expected non-empty result")
	}
	// Should contain the caption and a placeholder with "image" and "MIME".
	if got[:7] != "caption" {
		t.Errorf("expected to start with 'caption', got %q", got[:7])
	}
	if len(got) < 10 {
		t.Errorf("expected longer output, got %q", got)
	}
}

func TestFlattenBlocks_OnlyNonText(t *testing.T) {
	bs := Blocks{
		{Type: BlockImage, MediaSHA256: "a", MIME: "image/png", Size: 500},
	}
	got := FlattenBlocks(bs)
	if got == "" {
		t.Error("expected placeholder for non-text block, got empty string")
	}
}

// ---------------------------------------------------------------------------
// BlockTypeFromMIME
// ---------------------------------------------------------------------------

func TestBlockTypeFromMIME(t *testing.T) {
	tests := []struct {
		mime string
		want BlockType
	}{
		{"image/png", BlockImage},
		{"image/jpeg", BlockImage},
		{"audio/ogg", BlockAudio},
		{"audio/mpeg", BlockAudio},
		{"application/pdf", BlockDocument},
		{"text/plain", BlockDocument},
		{"unknown/x-binary", BlockDocument},
	}
	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			got := BlockTypeFromMIME(tt.mime)
			if got != tt.want {
				t.Errorf("BlockTypeFromMIME(%q) = %q, want %q", tt.mime, got, tt.want)
			}
		})
	}
}

func TestFlattenBlocks_Empty(t *testing.T) {
	got := FlattenBlocks(nil)
	if got != "" {
		t.Errorf("expected empty string for nil blocks, got %q", got)
	}
	got = FlattenBlocks(Blocks{})
	if got != "" {
		t.Errorf("expected empty string for empty blocks, got %q", got)
	}
}
