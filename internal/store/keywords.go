package store

import "strings"

// stopWords contains common English words that carry little semantic meaning
// and should be excluded from search keyword extraction.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
	"with": true, "by": true, "from": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "can": true,
	"it": true, "its": true, "this": true, "that": true, "these": true, "those": true,
	"i": true, "you": true, "he": true, "she": true, "we": true, "they": true,
	"what": true, "how": true, "when": true, "where": true, "why": true, "which": true,
	"not": true, "no": true, "if": true, "as": true, "so": true, "my": true,
	"your": true, "our": true, "their": true, "me": true, "him": true, "her": true,
	"us": true, "them": true, "about": true, "up": true, "out": true, "get": true,
	"use": true, "just": true, "than": true, "then": true, "also": true, "into": true,
}

// ExtractKeywords splits a query into meaningful keywords, stripping stop words
// and very short tokens. Returns unique keywords in original order.
// Preserves hyphens and underscores so code identifiers like auth_token or
// get-config are treated as single tokens.
func ExtractKeywords(query string) []string {
	// Split on whitespace and punctuation; keep alphanumeric plus hyphens/underscores.
	tokens := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') && r != '-' && r != '_'
	})

	seen := make(map[string]bool)
	var keywords []string
	for _, t := range tokens {
		if len(t) < 3 || stopWords[t] || seen[t] {
			continue
		}
		seen[t] = true
		keywords = append(keywords, t)
	}
	return keywords
}

// BuildFTSQuery builds an FTS5 MATCH query from keywords extracted from query.
// Each keyword is double-quoted to avoid FTS5 operator interpretation.
// Keywords are joined with OR so partial matches (any keyword) are returned.
// Returns empty string if no meaningful keywords are found (e.g. all stop words).
func BuildFTSQuery(query string) string {
	keywords := ExtractKeywords(query)
	if len(keywords) == 0 {
		return ""
	}
	parts := make([]string, len(keywords))
	for i, kw := range keywords {
		// Escape any embedded double-quotes by doubling them (FTS5 quoting rules).
		parts[i] = `"` + strings.ReplaceAll(kw, `"`, `""`) + `"`
	}
	return strings.Join(parts, " OR ")
}
