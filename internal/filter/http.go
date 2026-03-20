package filter

import (
	"regexp"
	"strings"
)

var (
	reHTMLTags    = regexp.MustCompile(`<[^>]+>`)
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reHTMLBlanks  = regexp.MustCompile(`\n{3,}`)
)

// FilterHTTP strips HTML markup from HTTP responses and applies generic
// truncation. For non-HTML responses only truncation is applied.
// Returns (filtered content, filter name).
func FilterHTTP(content string, limit int) (string, string) {
	// Sniff first 512 bytes to detect HTML
	sniff := content
	if len(sniff) > 512 {
		sniff = content[:512]
	}
	lower := strings.ToLower(sniff)
	isHTML := strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<html")

	if !isHTML {
		out, name := Truncate(content, limit)
		return out, name
	}

	// Strip <script> and <style> blocks first
	result := reScriptStyle.ReplaceAllString(content, "")
	// Strip all remaining HTML tags
	result = reHTMLTags.ReplaceAllString(result, "")
	// Collapse multiple blank lines
	result = reHTMLBlanks.ReplaceAllString(result, "\n\n")
	result = strings.TrimSpace(result)

	out, _ := Truncate(result, limit)
	return out, "http_html"
}
