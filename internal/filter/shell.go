package filter

import (
	"encoding/json"
	"strings"

	"daimon/internal/config"
)

type shellInput struct {
	Command string `json:"command"`
}

// applyShell dispatches a shell_exec result to the appropriate formatter based
// on the command string extracted from the JSON input.
// On JSON parse error or missing command field, falls through to generic truncation.
func applyShell(input json.RawMessage, content string, cfg config.FilterConfig) (string, string) {
	var si shellInput
	if err := json.Unmarshal(input, &si); err != nil || si.Command == "" {
		return Truncate(content, cfg.TruncationChars)
	}

	parts := strings.Fields(si.Command)
	if len(parts) == 0 {
		return Truncate(content, cfg.TruncationChars)
	}

	base := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch base {
	case "git":
		return applyGit(sub, content, cfg)
	case "ls", "find":
		return FormatListing(content)
	case "go":
		if sub == "test" {
			return FormatTestOutput(content), "go_test"
		}
	case "cargo":
		if sub == "test" {
			return FormatTestOutput(content), "cargo_test"
		}
	}

	return Truncate(content, cfg.TruncationChars)
}

func applyGit(sub, content string, cfg config.FilterConfig) (string, string) {
	switch sub {
	case "status":
		return FormatStatus(content), "git_status"
	case "diff":
		return FormatDiff(content), "git_diff"
	case "show":
		return FormatShow(content), "git_show"
	case "log":
		return FormatLog(content), "git_log"
	default:
		return Truncate(content, cfg.TruncationChars)
	}
}

// FormatTestOutput filters test runner output (go test / cargo test).
// Passing test lines are stripped; failures, panics, and the final summary are kept.
func FormatTestOutput(content string) string {
	var sb strings.Builder
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		if shouldKeepTestLine(line) {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func shouldKeepTestLine(line string) bool {
	// Never keep passing test lines
	if strings.HasPrefix(line, "--- PASS:") || strings.HasPrefix(line, "    --- PASS:") {
		return false
	}

	lower := strings.ToLower(line)
	// Always keep lines with failure/error/panic indicators
	failIndicators := []string{"fail", "error", "panic", "compilation", "build failed"}
	for _, ind := range failIndicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	// Keep "ok " summary lines (package-level pass summary)
	if strings.HasPrefix(line, "ok ") {
		return true
	}
	return false
}
