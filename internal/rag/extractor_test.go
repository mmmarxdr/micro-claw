package rag_test

import (
	"context"
	"errors"
	"testing"

	"microagent/internal/rag"
)

// T3.1 — PlainTextExtractor

func TestPlainTextExtractor_Supports(t *testing.T) {
	e := rag.PlainTextExtractor{}
	cases := []struct {
		mime string
		want bool
	}{
		{"text/plain", true},
		{"text/csv", true},
		{"text/html", true},
		{"text/markdown", true},
		{"application/json", false},
		{"image/png", false},
		{"", false},
	}
	for _, c := range cases {
		got := e.Supports(c.mime)
		if got != c.want {
			t.Errorf("PlainTextExtractor.Supports(%q) = %v, want %v", c.mime, got, c.want)
		}
	}
}

func TestPlainTextExtractor_Extract(t *testing.T) {
	e := rag.PlainTextExtractor{}
	ctx := context.Background()

	input := []byte("Hello, world!\nThis is plain text.")
	doc, err := e.Extract(ctx, input, "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Text != string(input) {
		t.Errorf("Text = %q, want %q", doc.Text, string(input))
	}
}

func TestPlainTextExtractor_ExtractUnsupported(t *testing.T) {
	e := rag.PlainTextExtractor{}
	ctx := context.Background()

	_, err := e.Extract(ctx, []byte("data"), "application/pdf")
	if !errors.Is(err, rag.ErrUnsupportedMIME) {
		t.Errorf("expected ErrUnsupportedMIME, got %v", err)
	}
}

// T3.1 — MarkdownExtractor

func TestMarkdownExtractor_Supports(t *testing.T) {
	e := rag.MarkdownExtractor{}
	cases := []struct {
		mime string
		want bool
	}{
		{"text/markdown", true},
		{"text/x-markdown", true},
		{"text/plain", false},
		{"application/json", false},
	}
	for _, c := range cases {
		got := e.Supports(c.mime)
		if got != c.want {
			t.Errorf("MarkdownExtractor.Supports(%q) = %v, want %v", c.mime, got, c.want)
		}
	}
}

func TestMarkdownExtractor_StripHeaders(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	input := "# Title\n## Section\n### Sub\nSome text"
	doc, err := e.Extract(ctx, []byte(input), "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(doc.Text, "#") {
		t.Errorf("expected headers to be stripped, got: %q", doc.Text)
	}
	if !contains(doc.Text, "Title") || !contains(doc.Text, "Section") || !contains(doc.Text, "Some text") {
		t.Errorf("expected text content preserved, got: %q", doc.Text)
	}
}

func TestMarkdownExtractor_StripBoldItalic(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	input := "**bold text** and *italic text* and __also bold__"
	doc, err := e.Extract(ctx, []byte(input), "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(doc.Text, "**") || contains(doc.Text, "__") {
		t.Errorf("expected bold markers stripped, got: %q", doc.Text)
	}
	if !contains(doc.Text, "bold text") || !contains(doc.Text, "italic text") {
		t.Errorf("expected text content preserved, got: %q", doc.Text)
	}
}

func TestMarkdownExtractor_StripLinks(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	input := "Check out [this link](https://example.com) for more."
	doc, err := e.Extract(ctx, []byte(input), "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(doc.Text, "https://example.com") {
		t.Errorf("expected link URL stripped, got: %q", doc.Text)
	}
	if !contains(doc.Text, "this link") {
		t.Errorf("expected link text preserved, got: %q", doc.Text)
	}
}

func TestMarkdownExtractor_StripCodeFences(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	input := "Before\n```go\nfunc main() {}\n```\nAfter"
	doc, err := e.Extract(ctx, []byte(input), "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if contains(doc.Text, "```") {
		t.Errorf("expected code fences stripped, got: %q", doc.Text)
	}
	if !contains(doc.Text, "Before") || !contains(doc.Text, "After") {
		t.Errorf("expected surrounding text preserved, got: %q", doc.Text)
	}
}

func TestMarkdownExtractor_StripBullets(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	input := "- item one\n- item two\n* item three"
	doc, err := e.Extract(ctx, []byte(input), "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(doc.Text, "item one") || !contains(doc.Text, "item two") {
		t.Errorf("expected list item text preserved, got: %q", doc.Text)
	}
}

func TestMarkdownExtractor_UnsupportedMIME(t *testing.T) {
	e := rag.MarkdownExtractor{}
	ctx := context.Background()

	_, err := e.Extract(ctx, []byte("data"), "text/plain")
	if !errors.Is(err, rag.ErrUnsupportedMIME) {
		t.Errorf("expected ErrUnsupportedMIME for text/plain, got %v", err)
	}
}

// T3.1 — SelectExtractor

func TestSelectExtractor_PicksMarkdown(t *testing.T) {
	ctx := context.Background()
	// MarkdownExtractor is listed before PlainTextExtractor so it's tried first for text/markdown.
	// This is necessary because PlainTextExtractor also accepts text/* (including text/markdown).
	sel := rag.NewSelectExtractor(rag.MarkdownExtractor{}, rag.PlainTextExtractor{})

	if !sel.Supports("text/markdown") {
		t.Error("SelectExtractor should support text/markdown")
	}

	input := []byte("# Header\nSome content")
	doc, err := sel.Extract(ctx, input, "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// MarkdownExtractor should have been used — headers stripped
	if contains(doc.Text, "#") {
		t.Errorf("expected MarkdownExtractor to be used (headers stripped), got: %q", doc.Text)
	}
}

func TestSelectExtractor_PicksPlainText(t *testing.T) {
	ctx := context.Background()
	sel := rag.NewSelectExtractor(rag.MarkdownExtractor{}, rag.PlainTextExtractor{})

	if !sel.Supports("text/plain") {
		t.Error("SelectExtractor should support text/plain")
	}

	input := []byte("hello plain")
	doc, err := sel.Extract(ctx, input, "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Text != string(input) {
		t.Errorf("Text = %q, want %q", doc.Text, string(input))
	}
}

func TestSelectExtractor_UnsupportedMIME(t *testing.T) {
	ctx := context.Background()
	sel := rag.NewSelectExtractor(rag.MarkdownExtractor{}, rag.PlainTextExtractor{})

	if sel.Supports("application/pdf") {
		t.Error("SelectExtractor should not support application/pdf")
	}

	_, err := sel.Extract(ctx, []byte("data"), "application/pdf")
	if !errors.Is(err, rag.ErrUnsupportedMIME) {
		t.Errorf("expected ErrUnsupportedMIME, got %v", err)
	}
}

func TestSelectExtractor_EmptyExtractors(t *testing.T) {
	ctx := context.Background()
	sel := rag.NewSelectExtractor()

	if sel.Supports("text/plain") {
		t.Error("empty SelectExtractor should not support anything")
	}
	_, err := sel.Extract(ctx, []byte("data"), "text/plain")
	if !errors.Is(err, rag.ErrUnsupportedMIME) {
		t.Errorf("expected ErrUnsupportedMIME, got %v", err)
	}
}

// helper
func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
