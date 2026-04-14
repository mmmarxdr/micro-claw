package content

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BlockType identifies the kind of content in a Block.
type BlockType string

const (
	BlockText     BlockType = "text"
	BlockImage    BlockType = "image"
	BlockAudio    BlockType = "audio"
	BlockDocument BlockType = "document"
)

// ContentBlock is a discriminated union representing one piece of multimodal content.
//
// For text blocks: Type == BlockText, Text is set, all media fields are zero.
// For media blocks: Type is BlockImage/BlockAudio/BlockDocument, MediaSHA256/MIME/Size are set,
// Text is empty. Filename is only set for document blocks.
type ContentBlock struct {
	Type        BlockType `json:"type"`
	Text        string    `json:"text,omitempty"`
	MediaSHA256 string    `json:"media_sha256,omitempty"`
	MIME        string    `json:"mime,omitempty"`
	Size        int64     `json:"size,omitempty"`
	Filename    string    `json:"filename,omitempty"`
}

// Blocks is a slice of ContentBlock. It is the canonical Content type used by
// ChatMessage (provider layer) and IncomingMessage (channel layer).
type Blocks []ContentBlock

// TextOnly concatenates the text of all text blocks, separated by newlines.
// Non-text blocks are skipped. Returns "" for empty or all-media slices.
func (bs Blocks) TextOnly() string {
	if len(bs) == 0 {
		return ""
	}
	var parts []string
	for _, b := range bs {
		if b.Type == BlockText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// Join with newline without importing strings at the package level.
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "\n" + parts[i]
	}
	return result
}

// HasMedia reports whether any block in bs is non-text (image, audio, document).
func (bs Blocks) HasMedia() bool {
	for _, b := range bs {
		if b.Type != BlockText {
			return true
		}
	}
	return false
}

// BlockTypeFromMIME returns the BlockType corresponding to a MIME type.
// "image/*" → BlockImage, "audio/*" → BlockAudio, everything else → BlockDocument.
func BlockTypeFromMIME(mime string) BlockType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return BlockImage
	case strings.HasPrefix(mime, "audio/"):
		return BlockAudio
	default:
		return BlockDocument
	}
}

// TextBlock is a convenience constructor that returns a single-block Blocks
// slice containing a plain text block.
func TextBlock(s string) Blocks {
	return Blocks{{Type: BlockText, Text: s}}
}

// UnmarshalBlocks decodes a raw JSON value that may be either a legacy plain
// string or the new array-of-blocks form, and returns a Blocks slice.
//
//   - nil / "null" / empty raw → (nil, nil)
//   - JSON string (first byte '"') → TextBlock(s)
//   - JSON array (first byte '[') → unmarshal as []ContentBlock
//   - anything else → error
func UnmarshalBlocks(raw json.RawMessage) (Blocks, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		return TextBlock(s), nil
	case '[':
		var bs Blocks
		if err := json.Unmarshal(raw, &bs); err != nil {
			return nil, err
		}
		return bs, nil
	default:
		// Reject numbers, booleans, objects — not valid content shapes.
		return nil, fmt.Errorf("content: unexpected JSON type for blocks field (first byte: %q)", raw[0])
	}
}
