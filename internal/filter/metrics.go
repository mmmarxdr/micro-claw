package filter

// Metrics holds token-savings statistics for a single filter application.
type Metrics struct {
	OriginalBytes   int
	CompressedBytes int
	FilterName      string // e.g. "git_diff", "file_minimal", "generic", "" (no-op)
}
