package filter

import (
	"strings"
)

// FormatStatus produces a compact git status representation.
// Hint lines and user-instruction boilerplate are stripped; file-state and
// branch lines are preserved.
func FormatStatus(content string) string {
	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		if shouldDropStatusLine(line) {
			continue
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func shouldDropStatusLine(line string) bool {
	lower := strings.ToLower(line)
	dropPhrases := []string{
		`use "git add"`,
		`use "git commit"`,
		`use "git push"`,
		`use "git restore"`,
		`use "git rm"`,
		`use "git checkout"`,
		`(use "git`,
		`(commit or`,
		`after resolving`,
	}
	for _, phrase := range dropPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	if strings.HasPrefix(line, "hint:") {
		return true
	}
	return false
}

// FormatDiff retains changed lines (+/-), hunk headers (@@), and file headers.
// Unchanged context lines (lines starting with a single space) are dropped.
func FormatDiff(content string) string {
	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		if keepDiffLine(line) {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func keepDiffLine(line string) bool {
	if line == "" {
		return false
	}
	// Keep added/removed lines
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
		return true
	}
	// Keep hunk headers
	if strings.HasPrefix(line, "@@") {
		return true
	}
	// Keep file header lines
	prefixes := []string{
		"diff --git",
		"diff -",
		"index ",
		"--- ",
		"+++ ",
		"Binary files",
		"Binary file",
		"new file mode",
		"deleted file mode",
		"rename from",
		"rename to",
		"similarity index",
		"old mode",
		"new mode",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// FormatLog reduces git log output to one line per commit.
// Lines beginning with "commit " are kept; author/date lines and blank
// separator lines between commits are kept; indented message body lines
// are kept only for the first non-empty body line per commit, then skipped
// until the next commit header.
func FormatLog(content string) string {
	var sb strings.Builder
	inBody := false
	bodyLineEmitted := false

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "commit ") {
			inBody = false
			bodyLineEmitted = false
			sb.WriteString(line)
			sb.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(line, "Author:") || strings.HasPrefix(line, "Date:") ||
			strings.HasPrefix(line, "Merge:") {
			sb.WriteString(line)
			sb.WriteByte('\n')
			continue
		}
		// Blank line between header and body
		if line == "" && !inBody {
			inBody = true
			continue
		}
		if inBody {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if !bodyLineEmitted {
				sb.WriteString("    ")
				sb.WriteString(trimmed)
				sb.WriteByte('\n')
				bodyLineEmitted = true
			}
			// Skip remaining body lines
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatShow applies FormatDiff logic — show is a diff with a commit header.
func FormatShow(content string) string {
	return FormatDiff(content)
}
