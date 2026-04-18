package config

import "regexp"

// MaskedPattern is the shared sentinel regex for masked API key values.
// A value matching this pattern is treated as a masked placeholder — do NOT write it.
// Format: up to 4 leading chars + "****" + up to 4 trailing chars.
//
// This is the single authoritative definition for the Go layer. The frontend
// has its own matching constant in src/lib/mask.ts.
var MaskedPattern = regexp.MustCompile(`^.{0,4}\*{4}.{0,4}$`)

// IsMasked reports whether s is a masked secret (matches MaskedPattern).
func IsMasked(s string) bool {
	return MaskedPattern.MatchString(s)
}

// MaskSecret returns a redacted version of s suitable for display.
// Secrets of 8 characters or fewer are fully replaced with "****".
// Longer secrets retain up to 4 leading and 4 trailing characters.
// Every output of MaskSecret matches MaskedPattern.
func MaskSecret(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}
