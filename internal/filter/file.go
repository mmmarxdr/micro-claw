package filter

import (
	"path/filepath"
	"regexp"
	"strings"
)

// dataFormats is the set of file extensions that must never be code-filtered.
var dataFormats = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".xml": true, ".csv": true, ".md": true, ".markdown": true, ".sql": true,
}

// codeFormats is the set of extensions that code-filtering is safe to apply to.
// Any extension not in this set is treated as unknown and passes through unchanged.
var codeFormats = map[string]bool{
	".go": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
	".py": true, ".rb": true, ".rs": true, ".java": true, ".kt": true,
	".c": true, ".cpp": true, ".cc": true, ".h": true, ".hpp": true,
	".cs": true, ".swift": true, ".php": true, ".sh": true, ".bash": true,
	".lua": true, ".scala": true, ".clj": true, ".ex": true, ".exs": true,
}

// reLineComment matches single-line C-style comments (// ...) and Python/shell comments (# ...).
var reLineComment = regexp.MustCompile(`(?m)^\s*(//|#)[^\n]*\n?`)

// reMultiBlank matches three or more consecutive blank lines.
var reMultiBlank = regexp.MustCompile(`\n{3,}`)

// reTrailingWS matches trailing whitespace before a newline.
var reTrailingWS = regexp.MustCompile(`[ \t]+\n`)

// FilterFileContent applies the configured filter level to a file's content.
// Data-format files and unknown extensions are returned unchanged regardless of level.
// Returns (filtered content, filter name).
func FilterFileContent(path, content, level string) (string, string) {
	ext := strings.ToLower(filepath.Ext(path))
	// Pass through: data formats, empty extension, unknown code format, or level disabled.
	if dataFormats[ext] || level == "no" || level == "" || ext == "" || !codeFormats[ext] {
		return content, "none"
	}
	switch level {
	case "minimal":
		return applyMinimalFilter(content), "file_minimal"
	case "aggressive":
		return applyAggressiveFilter(content), "file_aggressive"
	default:
		return content, "none"
	}
}

// applyMinimalFilter strips single-line comments, collapses multiple blank
// lines (3+ → 2), and strips trailing whitespace from each line.
func applyMinimalFilter(content string) string {
	// Strip single-line comments (// and #)
	result := reLineComment.ReplaceAllString(content, "\n")
	// Collapse 3+ blank lines to 2
	result = reMultiBlank.ReplaceAllString(result, "\n\n")
	// Strip trailing whitespace per line
	result = reTrailingWS.ReplaceAllString(result, "\n")
	return strings.TrimRight(result, "\n")
}

// applyAggressiveFilter retains only structural declarations: imports, function
// signatures, type/struct/interface definitions, const and var blocks.
// Function bodies are collapsed to "// ...".
func applyAggressiveFilter(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBody := false
	braceDepth := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inBody {
			// Count braces to track body end
			braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
			if braceDepth <= 0 {
				out = append(out, "}")
				inBody = false
				braceDepth = 0
			}
			continue
		}

		// Keep structural lines
		if isStructuralLine(trimmed) {
			out = append(out, line)
			// If line opens a block body (has a trailing {), enter body mode
			if strings.HasSuffix(trimmed, "{") && !strings.HasPrefix(trimmed, "//") {
				out = append(out, "\t// ...")
				braceDepth = 1
				inBody = true
			}
			continue
		}

		// Keep package declaration and import blocks
		if strings.HasPrefix(trimmed, "package ") ||
			strings.HasPrefix(trimmed, "import ") ||
			trimmed == "import (" ||
			(len(out) > 0 && isInImportBlock(out)) {
			out = append(out, line)
			continue
		}

		// Keep blank lines between declarations for readability
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
		}
	}

	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// isStructuralLine returns true for lines that should be retained in aggressive mode.
func isStructuralLine(trimmed string) bool {
	structural := []string{
		"func ",
		"type ",
		"const ",
		"const(",
		"var ",
		"var(",
	}
	for _, prefix := range structural {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	// Closing braces at top level
	if trimmed == ")" || trimmed == "}" {
		return true
	}
	return false
}

// isInImportBlock checks whether we are currently inside an import (...) block
// by scanning backwards in the accumulated output.
func isInImportBlock(out []string) bool {
	depth := 0
	for i := len(out) - 1; i >= 0; i-- {
		t := strings.TrimSpace(out[i])
		if t == ")" {
			depth++
		}
		if t == "import (" {
			if depth == 0 {
				return true
			}
			depth--
		}
		if t == ")" && depth > 0 {
			return false
		}
	}
	return false
}
