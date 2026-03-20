package filter

import "fmt"

// Truncate applies first-70%/last-30% truncation at the given character limit.
// Returns content unchanged (and filter name "none") if len(content) <= limit.
// Returns the truncated content and filter name "generic_truncate" otherwise.
func Truncate(content string, limit int) (string, string) {
	if limit <= 0 || len(content) <= limit {
		return content, "none"
	}
	firstPart := int(float64(limit) * 0.70)
	lastPart := limit - firstPart
	omitted := len(content) - firstPart - lastPart
	result := content[:firstPart] +
		fmt.Sprintf("\n...[%d chars omitted]...\n", omitted) +
		content[len(content)-lastPart:]
	return result, "generic_truncate"
}
