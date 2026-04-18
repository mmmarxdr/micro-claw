package config

import (
	"strings"
	"testing"
)

func TestMaskSecret_ShortSecret(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "****"},
		{"ab", "****"},
		{"1234567", "****"},  // 7 chars — ≤ 8
		{"12345678", "****"}, // exactly 8 — still short (≤ 8)
	}
	for _, tc := range tests {
		got := MaskSecret(tc.input)
		if got != tc.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if !IsMasked(got) {
			t.Errorf("MaskSecret(%q) output %q is not recognized by IsMasked", tc.input, got)
		}
	}
}

func TestMaskSecret_LongSecret(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-ant-api03-ABCDEFGHIJKLMNOP", "sk-a****MNOP"},
		{"sk-or-v1-ABCDEFGH12345678", "sk-o****5678"},
		{"AIzaSyABCDEFGHIJKLMNOP", "AIza****MNOP"},
		{"sk-proj-ABCDEFGH12345678", "sk-p****5678"},
		{"verylongsecretkey9999", "very****9999"},
	}
	for _, tc := range tests {
		got := MaskSecret(tc.input)
		if got != tc.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if !IsMasked(got) {
			t.Errorf("MaskSecret(%q) output %q not recognized by IsMasked", tc.input, got)
		}
	}
}

func TestIsMasked_TruthTable(t *testing.T) {
	// Masked outputs — must return true.
	masked := []string{
		"****",
		"sk-a****1234",
		"sk-o****a68c",
		"AIza****MNOP",
		"sk-p****5678",
	}
	for _, s := range masked {
		if !IsMasked(s) {
			t.Errorf("IsMasked(%q) = false, want true", s)
		}
	}

	// Raw keys — must return false.
	rawKeys := []string{
		"sk-ant-api03-ABCDEFGHIJKLMNOP",
		"sk-or-v1-ABCDEFGH12345678",
		"AIzaSyABCDEFGHIJKLMNOP",
		"sk-proj-ABCDEFGH12345678",
		"",                        // empty string — does NOT match (no ****)
		"hello",                   // short plain string
		"sk-ant-XXXX-not-masked",  // no **** in right position
	}
	for _, s := range rawKeys {
		if IsMasked(s) {
			t.Errorf("IsMasked(%q) = true, want false", s)
		}
	}
}

// TestMaskedPattern_KnownKeyFormats — fuzz-ish table confirming no real key
// falsely matches MaskedPattern, and every MaskSecret output does match it.
func TestMaskedPattern_KnownKeyFormats(t *testing.T) {
	realKeys := []struct {
		name string
		key  string
	}{
		{"anthropic", "sk-ant-api03-" + strings.Repeat("A", 43)},
		{"openai", "sk-proj-" + strings.Repeat("B", 48)},
		{"gemini", "AIzaSy" + strings.Repeat("C", 35)},
		{"openrouter", "sk-or-v1-" + strings.Repeat("D", 64)},
		{"ollama-empty", ""},
		{"ollama-arbitrary", "anything-custom"},
	}

	for _, tc := range realKeys {
		t.Run(tc.name+"_real_key_not_masked", func(t *testing.T) {
			if IsMasked(tc.key) {
				t.Errorf("real key for %s matches MaskedPattern — false positive: %q", tc.name, tc.key)
			}
		})
		t.Run(tc.name+"_masked_output_is_masked", func(t *testing.T) {
			if tc.key == "" {
				// empty → MaskSecret returns "****" which IS masked
				got := MaskSecret(tc.key)
				if !IsMasked(got) {
					t.Errorf("MaskSecret(%q) = %q, IsMasked = false", tc.key, got)
				}
				return
			}
			got := MaskSecret(tc.key)
			if !IsMasked(got) {
				t.Errorf("MaskSecret output %q for %s not recognized by IsMasked", got, tc.name)
			}
		})
	}
}
