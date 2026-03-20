package filter

import (
	"fmt"
	"strings"
)

// FormatListing groups a directory listing output: directories first, then
// files. A summary line "N dirs, M files" is appended.
// Returns the formatted content and filter name "listing".
func FormatListing(content string) (string, string) {
	var dirs []string
	var files []string
	var other []string // totals / header lines (e.g. "total 48")

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Skip "total N" header lines from ls -la
		if strings.HasPrefix(line, "total ") {
			other = append(other, line)
			continue
		}

		name := extractName(line)
		if name == "" || name == "." || name == ".." {
			continue
		}

		// Detect directories: ls -la shows "d" as first char, or name ends with "/"
		if isDirectory(line, name) {
			dirs = append(dirs, line)
		} else {
			files = append(files, line)
		}
	}

	var sb strings.Builder
	for _, l := range other {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	for _, l := range dirs {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	for _, l := range files {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	sb.WriteString(fmt.Sprintf("%d dirs, %d files", len(dirs), len(files)))

	return sb.String(), "listing"
}

// isDirectory detects whether a listing line represents a directory.
func isDirectory(line, name string) bool {
	// ls -la: first character is 'd'
	if len(line) > 0 && line[0] == 'd' {
		return true
	}
	// find output or list_files output: name ends with "/"
	if strings.HasSuffix(name, "/") {
		return true
	}
	// list_files style: name followed by " (dir)" or similar
	if strings.Contains(line, "(dir)") {
		return true
	}
	return false
}

// extractName extracts the file/dir name from a listing line.
// Handles:
//   - ls -la: last whitespace-separated field
//   - find: the full path as-is
//   - list_files: "name (N bytes)" or just "name"
func extractName(line string) string {
	// If line looks like ls -la (starts with permission bits or "d/l/-")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	// ls -la has at least 9 fields; last field is name (may be "name -> target")
	if len(fields) >= 9 && (line[0] == '-' || line[0] == 'd' || line[0] == 'l' || line[0] == 'b' || line[0] == 'c') {
		name := fields[len(fields)-1]
		// Handle symlinks: "name -> target" — take "name" part
		if idx := strings.Index(name, " -> "); idx >= 0 {
			name = name[:idx]
		}
		return name
	}
	// list_files style: "filename (N bytes)" — strip the annotation
	name := fields[0]
	if strings.HasSuffix(name, "(") {
		// "filename" "(" — join is odd; just return first field trimmed of "("
		name = strings.TrimSuffix(name, "(")
	}
	return name
}
