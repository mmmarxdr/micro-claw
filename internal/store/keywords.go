package store

import "strings"

// synonyms maps common abbreviations and short forms to their expanded terms.
// Used by BuildFTSQuery to broaden recall by adding OR clauses for the
// expanded form. The original token is always preserved alongside the expansion.
//
// Design note: 2-char entries (e.g. "db") are also used by ExtractKeywords to
// decide whether to keep short tokens — any 2-char token in this map survives
// the minimum-length filter so that its synonym expansion can fire.
var synonyms = map[string]string{
	"auth": "authentication",
	"db":   "database",
	"cfg":  "config",
	"pwd":  "password",
	"env":  "environment",
	"repo": "repository",
	"msg":  "message",
}

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
		// Allow 2-char tokens if they appear in the synonym map (e.g. "db"),
		// otherwise keep the minimum length at 3 to filter noise.
		if len(t) < 2 || stopWords[t] || seen[t] {
			continue
		}
		if len(t) == 2 && synonyms[t] == "" {
			// 2-char token not in synonym map — filter out.
			continue
		}
		seen[t] = true
		keywords = append(keywords, t)
	}
	return keywords
}

// BuildFTSQuery builds an FTS5 MATCH query from keywords extracted from query.
//
// Each keyword is double-quoted to avoid FTS5 operator interpretation. Keywords
// are joined with OR so partial matches (any keyword) are returned.
//
// Two enhancements are applied:
//   - Prefix matching: keywords longer than 4 characters get a trailing * so
//     that "config" matches "configuration", "configured", etc.
//   - Synonym expansion: known abbreviations (e.g. "db") add the expanded form
//     as an additional OR clause (e.g. "database"*) without replacing the original.
//
// Returns empty string if no meaningful keywords are found (e.g. all stop words).
func BuildFTSQuery(query string) string {
	keywords := ExtractKeywords(query)
	if len(keywords) == 0 {
		return ""
	}

	var parts []string
	seen := make(map[string]bool)

	for _, kw := range keywords {
		// Escape any embedded double-quotes by doubling them (FTS5 quoting rules).
		escaped := strings.ReplaceAll(kw, `"`, `""`)

		// Prefix matching: keywords longer than 4 characters use "term"* syntax.
		if len(kw) > 4 {
			parts = append(parts, `"`+escaped+`"*`)
		} else {
			parts = append(parts, `"`+escaped+`"`)
		}
		seen[kw] = true

		// Synonym expansion: add the expanded form as an additional OR clause.
		// Synonyms are always prefix-matched (expanded terms tend to be long).
		if expanded, ok := synonyms[kw]; ok && !seen[expanded] {
			seen[expanded] = true
			expandedEscaped := strings.ReplaceAll(expanded, `"`, `""`)
			parts = append(parts, `"`+expandedEscaped+`"*`)
		}
	}

	return strings.Join(parts, " OR ")
}
