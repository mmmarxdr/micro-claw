package rag

import (
	"fmt"
	"strings"
)

// Chunker splits a block of text into DocumentChunks.
// Implementations should be stateless and safe for concurrent use.
type Chunker interface {
	// Chunk splits text according to opts and returns the resulting chunks.
	// Returned chunks do not have DocID or Embedding set — callers must fill those.
	Chunk(text string, opts ChunkOptions) []DocumentChunk
}

// ─── FixedSizeChunker ────────────────────────────────────────────────────────

// FixedSizeChunker splits text into fixed-size rune chunks with overlap.
// At each boundary it attempts to snap to a natural break point (paragraph,
// sentence, or word) before making a hard cut.
type FixedSizeChunker struct{}

// Chunk splits text into DocumentChunks according to opts.
// It uses rune-aware iteration and snaps chunk boundaries to natural breaks.
func (c FixedSizeChunker) Chunk(text string, opts ChunkOptions) []DocumentChunk {
	// Apply defaults for zero-value options.
	if opts.Size <= 0 {
		opts.Size = 512
	}
	if opts.Overlap < 0 {
		opts.Overlap = 0
	}
	if opts.Overlap >= opts.Size {
		opts.Overlap = opts.Size / 4
	}

	runes := []rune(text)
	total := len(runes)
	if total == 0 {
		return nil
	}

	// If text fits in one chunk, return it directly.
	if total <= opts.Size {
		return []DocumentChunk{
			{
				ID:      "0",
				Index:   0,
				Content: text,
			},
		}
	}

	var chunks []DocumentChunk
	start := 0
	index := 0
	snapRange := opts.Size / 5 // 20% of chunk size for snapping

	for start < total {
		end := start + opts.Size
		if end > total {
			end = total
		}

		// Try to snap boundary backward to a natural break, unless we're at end.
		if end < total {
			end = snapBoundary(runes, end, snapRange)
		}

		content := string(runes[start:end])
		chunks = append(chunks, DocumentChunk{
			ID:      fmt.Sprintf("%d", index),
			Index:   index,
			Content: content,
		})
		index++

		// Advance start, accounting for overlap.
		next := end - opts.Overlap
		if next <= start {
			// Prevent infinite loop — advance at least 1 rune.
			next = start + 1
		}
		start = next
	}

	return chunks
}

// snapBoundary tries to find a better break point near pos in runes.
// It searches backward up to snapRange runes for (in priority order):
//  1. Paragraph break (\n\n)
//  2. Sentence end (". ")
//  3. Word boundary (" ")
//
// Returns pos unchanged if no better break is found.
func snapBoundary(runes []rune, pos, snapRange int) int {
	if snapRange <= 0 {
		return pos
	}
	lo := pos - snapRange
	if lo < 0 {
		lo = 0
	}

	// Priority 1: paragraph break (\n\n)
	for i := pos - 1; i >= lo+1; i-- {
		if runes[i] == '\n' && runes[i-1] == '\n' {
			return i + 1 // start of the next paragraph
		}
	}

	// Priority 2: sentence end (". ")
	// Look for a period followed by a space.
	s := string(runes[lo:pos])
	if idx := strings.LastIndex(s, ". "); idx >= 0 {
		return lo + idx + 2 // position after ". "
	}

	// Priority 3: word boundary (space)
	for i := pos - 1; i >= lo; i-- {
		if runes[i] == ' ' {
			return i + 1
		}
	}

	// Hard cut — return original pos.
	return pos
}
