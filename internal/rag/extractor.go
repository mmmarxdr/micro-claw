package rag

import (
	"context"
	"regexp"
	"strings"
)

// Extractor converts raw bytes (e.g. a PDF or HTML blob) into an ExtractedDoc.
// Implementations should be stateless and safe for concurrent use.
type Extractor interface {
	// Extract parses data of the given MIME type and returns plain text plus a title.
	Extract(ctx context.Context, data []byte, mime string) (ExtractedDoc, error)

	// Supports reports whether this extractor handles the given MIME type.
	Supports(mime string) bool
}

// ─── PlainTextExtractor ──────────────────────────────────────────────────────

// PlainTextExtractor handles text/* MIME types. Returns content as-is.
type PlainTextExtractor struct{}

// Supports returns true for any text/* MIME type.
func (e PlainTextExtractor) Supports(mime string) bool {
	return strings.HasPrefix(mime, "text/")
}

// Extract returns the data unchanged as the document text.
func (e PlainTextExtractor) Extract(_ context.Context, data []byte, mime string) (ExtractedDoc, error) {
	if !e.Supports(mime) {
		return ExtractedDoc{}, ErrUnsupportedMIME
	}
	return ExtractedDoc{Text: string(data)}, nil
}

// ─── MarkdownExtractor ───────────────────────────────────────────────────────

// MarkdownExtractor strips markdown syntax for better chunking/embedding.
type MarkdownExtractor struct{}

// Supports returns true for text/markdown and text/x-markdown.
func (e MarkdownExtractor) Supports(mime string) bool {
	return mime == "text/markdown" || mime == "text/x-markdown"
}

var (
	reCodeFence   = regexp.MustCompile("(?m)^```[^\n]*\n(.*?\n)?```")
	reHeader      = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	reBoldItalic3 = regexp.MustCompile(`\*{3}([^*]+)\*{3}`)
	reBoldItalic2 = regexp.MustCompile(`\*{2}([^*]+)\*{2}`)
	reItalic1     = regexp.MustCompile(`\*([^*]+)\*`)
	reBoldU2      = regexp.MustCompile(`__([^_]+)__`)
	reItalicU1    = regexp.MustCompile(`_([^_]+)_`)
	reLink        = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	reBullet      = regexp.MustCompile(`(?m)^[*\-+]\s+`)
	reOrderedList = regexp.MustCompile(`(?m)^\d+\.\s+`)
)

// Extract strips markdown syntax and returns plain text.
func (e MarkdownExtractor) Extract(_ context.Context, data []byte, mime string) (ExtractedDoc, error) {
	if !e.Supports(mime) {
		return ExtractedDoc{}, ErrUnsupportedMIME
	}

	text := string(data)

	// Strip code fences (remove fences, keep content between them)
	text = reCodeFence.ReplaceAllStringFunc(text, func(match string) string {
		// Strip opening and closing ``` lines, keep interior
		lines := strings.Split(match, "\n")
		if len(lines) <= 2 {
			return ""
		}
		return strings.Join(lines[1:len(lines)-1], "\n")
	})

	// Strip headers (remove # markers, keep text)
	text = reHeader.ReplaceAllString(text, "")

	// Strip bold+italic (***text*** → text)
	text = reBoldItalic3.ReplaceAllString(text, "$1")
	// Strip bold (**text** → text)
	text = reBoldItalic2.ReplaceAllString(text, "$1")
	// Strip italic (*text* → text)
	text = reItalic1.ReplaceAllString(text, "$1")
	// Strip bold (__text__ → text)
	text = reBoldU2.ReplaceAllString(text, "$1")
	// Strip italic (_text_ → text)
	text = reItalicU1.ReplaceAllString(text, "$1")

	// Strip links [text](url) → text
	text = reLink.ReplaceAllString(text, "$1")

	// Strip bullet markers
	text = reBullet.ReplaceAllString(text, "")
	text = reOrderedList.ReplaceAllString(text, "")

	return ExtractedDoc{Text: strings.TrimSpace(text)}, nil
}

// ─── SelectExtractor ─────────────────────────────────────────────────────────

// SelectExtractor tries extractors in order and returns the first match.
type SelectExtractor struct {
	extractors []Extractor
}

// NewSelectExtractor constructs a SelectExtractor from the given extractors.
func NewSelectExtractor(extractors ...Extractor) *SelectExtractor {
	return &SelectExtractor{extractors: extractors}
}

// Supports returns true if any inner extractor supports the MIME type.
func (e *SelectExtractor) Supports(mime string) bool {
	for _, ex := range e.extractors {
		if ex.Supports(mime) {
			return true
		}
	}
	return false
}

// Extract calls the first extractor that supports the MIME type.
func (e *SelectExtractor) Extract(ctx context.Context, data []byte, mime string) (ExtractedDoc, error) {
	for _, ex := range e.extractors {
		if ex.Supports(mime) {
			return ex.Extract(ctx, data, mime)
		}
	}
	return ExtractedDoc{}, ErrUnsupportedMIME
}
