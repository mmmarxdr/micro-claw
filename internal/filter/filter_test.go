package filter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/tool"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func enabledCfg() config.FilterConfig {
	return config.FilterConfig{
		Enabled:         true,
		TruncationChars: 8000,
		Levels: config.FilterLevels{
			Shell:    "aggressive",
			FileRead: "minimal",
			Generic:  true,
		},
	}
}

func makeShellInput(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func textResult(content string) tool.ToolResult {
	return tool.ToolResult{Content: content}
}

// ---------------------------------------------------------------------------
// Task 8.7 — Apply() disabled → no-op
// ---------------------------------------------------------------------------

func TestApply_DisabledIsNoop(t *testing.T) {
	cfg := config.FilterConfig{Enabled: false, TruncationChars: 100}
	bigContent := strings.Repeat("x", 200)
	result := textResult(bigContent)

	got, metrics := Apply("shell_exec", makeShellInput("git diff"), result, cfg)
	if got.Content != bigContent {
		t.Errorf("disabled: content modified, want unchanged")
	}
	if metrics.OriginalBytes != 0 || metrics.CompressedBytes != 0 || metrics.FilterName != "" {
		t.Errorf("disabled: non-zero metrics: %+v", metrics)
	}
}

// ---------------------------------------------------------------------------
// Task 8.8 — Apply() with IsError=true → pass-through
// ---------------------------------------------------------------------------

func TestApply_ErrorResultPassthrough(t *testing.T) {
	cfg := enabledCfg()
	errResult := tool.ToolResult{IsError: true, Content: "some error message"}
	got, metrics := Apply("shell_exec", makeShellInput("git diff"), errResult, cfg)
	if got.Content != errResult.Content {
		t.Errorf("error result: content changed, want unchanged")
	}
	if metrics.FilterName != "" {
		t.Errorf("error result: expected empty FilterName, got %q", metrics.FilterName)
	}
}

// ---------------------------------------------------------------------------
// Task 8.7 (continued) — Apply() each tool type with enabled=false
// ---------------------------------------------------------------------------

func TestApply_AllToolsDisabled(t *testing.T) {
	cfg := config.FilterConfig{Enabled: false, TruncationChars: 8000}
	tools := []string{"shell_exec", "read_file", "list_files", "http_fetch", "write_file", "mcp__some__tool"}
	content := strings.Repeat("a", 100)
	for _, toolName := range tools {
		t.Run(toolName, func(t *testing.T) {
			got, m := Apply(toolName, json.RawMessage(`{}`), textResult(content), cfg)
			if got.Content != content {
				t.Errorf("%s: content modified when disabled", toolName)
			}
			if m.OriginalBytes != 0 {
				t.Errorf("%s: expected zero metrics", toolName)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 8.1 — Apply() dispatch table
// ---------------------------------------------------------------------------

func TestApply_ShellExec_GitDiff(t *testing.T) {
	cfg := enabledCfg()
	diffContent := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,5 @@
 package main

-func old() {}
+func new() {}

 // context line
 // more context`

	got, metrics := Apply("shell_exec", makeShellInput("git diff HEAD~1"), textResult(diffContent), cfg)
	if strings.Contains(got.Content, "// context line") {
		t.Errorf("git diff: context lines should be removed")
	}
	if !strings.Contains(got.Content, "+func new()") {
		t.Errorf("git diff: added lines should be retained")
	}
	if !strings.Contains(got.Content, "-func old()") {
		t.Errorf("git diff: removed lines should be retained")
	}
	if metrics.FilterName != "git_diff" {
		t.Errorf("git diff: FilterName = %q, want git_diff", metrics.FilterName)
	}
}

func TestApply_ShellExec_GitStatus(t *testing.T) {
	cfg := enabledCfg()
	statusContent := `On branch main
Your branch is up to date with 'origin/main'.

Changes not staged for commit:
  (use "git add <file>..." to update what will be committed)
  (use "git restore <file>..." to discard changes in working directory)
	modified:   internal/foo.go

Untracked files:
  (use "git add <file>..." to include in what will be committed)
	README.md

no changes added to commit (use "git add" and/or "git commit -a")`

	got, metrics := Apply("shell_exec", makeShellInput("git status"), textResult(statusContent), cfg)
	if strings.Contains(got.Content, `use "git add"`) {
		t.Errorf("git status: hint lines should be stripped")
	}
	if !strings.Contains(got.Content, "modified:") {
		t.Errorf("git status: file state lines should be kept")
	}
	if metrics.FilterName != "git_status" {
		t.Errorf("git status: FilterName = %q, want git_status", metrics.FilterName)
	}
}

func TestApply_ShellExec_GitLog(t *testing.T) {
	cfg := enabledCfg()
	logContent := `commit abc123
Author: Alice <alice@example.com>
Date:   Mon Mar 20 10:00:00 2026

    First commit message line
    More details here

commit def456
Author: Bob <bob@example.com>
Date:   Sun Mar 19 09:00:00 2026

    Second commit`

	got, metrics := Apply("shell_exec", makeShellInput("git log"), textResult(logContent), cfg)
	if !strings.Contains(got.Content, "commit abc123") {
		t.Errorf("git log: commit hash lines should be kept")
	}
	if metrics.FilterName != "git_log" {
		t.Errorf("git log: FilterName = %q, want git_log", metrics.FilterName)
	}
}

func TestApply_ShellExec_Ls(t *testing.T) {
	cfg := enabledCfg()
	lsContent := `total 48
drwxr-xr-x  5 user group 4096 Mar 20 10:00 .
drwxr-xr-x 10 user group 4096 Mar 20 09:00 ..
drwxr-xr-x  3 user group 4096 Mar 20 10:00 internal
-rw-r--r--  1 user group  512 Mar 20 10:00 README.md
-rw-r--r--  1 user group 1024 Mar 20 10:00 main.go`

	got, metrics := Apply("shell_exec", makeShellInput("ls -la"), textResult(lsContent), cfg)
	if !strings.Contains(got.Content, "dirs,") {
		t.Errorf("ls: output should contain summary line with 'dirs,', got: %q", got.Content)
	}
	if metrics.FilterName != "listing" {
		t.Errorf("ls: FilterName = %q, want listing", metrics.FilterName)
	}
}

func TestApply_ShellExec_UnknownCommand_BelowThreshold(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 8000
	small := strings.Repeat("x", 100)
	got, metrics := Apply("shell_exec", makeShellInput("jq . output.json"), textResult(small), cfg)
	if got.Content != small {
		t.Errorf("unknown cmd below threshold: content should be unchanged")
	}
	if metrics.FilterName != "none" {
		t.Errorf("unknown cmd below threshold: FilterName = %q, want none", metrics.FilterName)
	}
}

func TestApply_ShellExec_UnknownCommand_AboveThreshold(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 100
	big := strings.Repeat("x", 500)
	got, metrics := Apply("shell_exec", makeShellInput("jq . big.json"), textResult(big), cfg)
	if !strings.Contains(got.Content, "chars omitted") {
		t.Errorf("unknown cmd above threshold: truncation marker expected, got: %q", got.Content)
	}
	if metrics.FilterName != "generic_truncate" {
		t.Errorf("unknown cmd above threshold: FilterName = %q, want generic_truncate", metrics.FilterName)
	}
}

func TestApply_ShellExec_GoTest(t *testing.T) {
	cfg := enabledCfg()
	testOutput := `--- PASS: TestFoo (0.01s)
--- PASS: TestBar (0.02s)
--- FAIL: TestBaz (0.00s)
    foo_test.go:12: expected true, got false
FAIL
ok  	microagent/internal/foo	0.05s`

	got, metrics := Apply("shell_exec", makeShellInput("go test ./..."), textResult(testOutput), cfg)
	if strings.Contains(got.Content, "--- PASS:") {
		t.Errorf("go test: passing lines should be stripped")
	}
	if !strings.Contains(got.Content, "FAIL") {
		t.Errorf("go test: FAIL lines should be kept")
	}
	if metrics.FilterName != "go_test" {
		t.Errorf("go test: FilterName = %q, want go_test", metrics.FilterName)
	}
}

func TestApply_ReadFile_GoMinimal(t *testing.T) {
	cfg := enabledCfg()
	goContent := `package foo

// This is a comment
func Hello() string {
	return "hello"
}


// Another comment`

	input, _ := json.Marshal(map[string]string{"path": "internal/foo/foo.go"})
	got, metrics := Apply("read_file", input, textResult(goContent), cfg)
	if strings.Contains(got.Content, "// This is a comment") {
		t.Errorf("read_file minimal: single-line comments should be stripped")
	}
	if !strings.Contains(got.Content, "func Hello") {
		t.Errorf("read_file minimal: function signatures should be retained")
	}
	if metrics.FilterName != "file_minimal" {
		t.Errorf("read_file minimal: FilterName = %q, want file_minimal", metrics.FilterName)
	}
}

func TestApply_ReadFile_YAML_Passthrough(t *testing.T) {
	cfg := enabledCfg()
	cfg.Levels.FileRead = "aggressive"
	yamlContent := `key: value
nested:
  a: 1`
	input, _ := json.Marshal(map[string]string{"path": "config.yaml"})
	got, metrics := Apply("read_file", input, textResult(yamlContent), cfg)
	if got.Content != yamlContent {
		t.Errorf("read_file yaml: data format should pass through unchanged")
	}
	if metrics.FilterName != "none" {
		t.Errorf("read_file yaml: FilterName = %q, want none", metrics.FilterName)
	}
}

func TestApply_ReadFile_UnknownExt_Passthrough(t *testing.T) {
	cfg := enabledCfg()
	content := "some unknown content"
	input, _ := json.Marshal(map[string]string{"path": "file.xyz"})
	got, _ := Apply("read_file", input, textResult(content), cfg)
	if got.Content != content {
		t.Errorf("read_file unknown ext: should pass through unchanged")
	}
}

func TestApply_ListFiles(t *testing.T) {
	cfg := enabledCfg()
	content := `internal/foo.go
internal/bar.go
configs/`

	got, metrics := Apply("list_files", json.RawMessage(`{}`), textResult(content), cfg)
	if !strings.Contains(got.Content, "dirs,") {
		t.Errorf("list_files: summary line expected")
	}
	if metrics.FilterName != "listing" {
		t.Errorf("list_files: FilterName = %q, want listing", metrics.FilterName)
	}
}

func TestApply_HTTPFetch_HTML(t *testing.T) {
	cfg := enabledCfg()
	htmlContent := `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>Some text here.</p>
</body>
</html>`

	got, metrics := Apply("http_fetch", json.RawMessage(`{}`), textResult(htmlContent), cfg)
	if strings.Contains(got.Content, "<html>") || strings.Contains(got.Content, "<p>") {
		t.Errorf("http_fetch HTML: tags should be stripped")
	}
	if !strings.Contains(got.Content, "Hello World") {
		t.Errorf("http_fetch HTML: visible text should be preserved")
	}
	if metrics.FilterName != "http_html" {
		t.Errorf("http_fetch HTML: FilterName = %q, want http_html", metrics.FilterName)
	}
}

func TestApply_HTTPFetch_JSON(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 8000
	jsonContent := `{"key": "value", "number": 42}`
	got, metrics := Apply("http_fetch", json.RawMessage(`{}`), textResult(jsonContent), cfg)
	if got.Content != jsonContent {
		t.Errorf("http_fetch JSON: non-HTML should pass through (below limit)")
	}
	if metrics.FilterName != "none" {
		t.Errorf("http_fetch JSON: FilterName = %q, want none", metrics.FilterName)
	}
}

func TestApply_WriteFile_Passthrough(t *testing.T) {
	cfg := enabledCfg()
	content := "File written successfully."
	got, _ := Apply("write_file", json.RawMessage(`{}`), textResult(content), cfg)
	if got.Content != content {
		t.Errorf("write_file: should always pass through")
	}
}

func TestApply_MCPTool_BelowThreshold(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 8000
	small := strings.Repeat("a", 100)
	got, metrics := Apply("mcp__some_tool__action", json.RawMessage(`{}`), textResult(small), cfg)
	if got.Content != small {
		t.Errorf("MCP below threshold: content should be unchanged")
	}
	if metrics.FilterName != "none" {
		t.Errorf("MCP below threshold: FilterName = %q, want none", metrics.FilterName)
	}
}

func TestApply_MCPTool_AboveThreshold(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 100
	big := strings.Repeat("a", 500)
	got, metrics := Apply("mcp__some_tool__action", json.RawMessage(`{}`), textResult(big), cfg)
	if !strings.Contains(got.Content, "chars omitted") {
		t.Errorf("MCP above threshold: truncation marker expected")
	}
	if metrics.FilterName != "generic_truncate" {
		t.Errorf("MCP above threshold: FilterName = %q, want generic_truncate", metrics.FilterName)
	}
}

// ---------------------------------------------------------------------------
// Task 8.9 — Malformed JSON input for shell_exec → no panic
// ---------------------------------------------------------------------------

func TestApply_ShellExec_MalformedJSON(t *testing.T) {
	cfg := enabledCfg()
	cfg.TruncationChars = 200
	big := strings.Repeat("x", 500)
	// Should not panic; should fall through to generic truncation
	got, metrics := Apply("shell_exec", json.RawMessage(`not valid json`), textResult(big), cfg)
	if got.Content == "" {
		t.Errorf("malformed JSON: result should not be empty")
	}
	if metrics.FilterName == "" {
		t.Errorf("malformed JSON: expected a filter name")
	}
}

// ---------------------------------------------------------------------------
// Task 8.2 — Truncate()
// ---------------------------------------------------------------------------

func TestTruncate_BelowLimit(t *testing.T) {
	content := strings.Repeat("a", 100)
	got, name := Truncate(content, 200)
	if got != content {
		t.Errorf("below limit: content should be unchanged")
	}
	if name != "none" {
		t.Errorf("below limit: name = %q, want none", name)
	}
}

func TestTruncate_ExactlyAtLimit(t *testing.T) {
	content := strings.Repeat("a", 100)
	got, name := Truncate(content, 100)
	if got != content {
		t.Errorf("at limit: content should be unchanged")
	}
	if name != "none" {
		t.Errorf("at limit: name = %q, want none", name)
	}
}

func TestTruncate_AboveLimit(t *testing.T) {
	limit := 100
	content := strings.Repeat("a", 50) + strings.Repeat("b", 300) + strings.Repeat("z", 50)
	got, name := Truncate(content, limit)
	if !strings.Contains(got, "chars omitted") {
		t.Errorf("above limit: truncation marker missing")
	}
	if name != "generic_truncate" {
		t.Errorf("above limit: name = %q, want generic_truncate", name)
	}
	// First 70 chars of limit should be from content[:70]
	if !strings.HasPrefix(got, content[:70]) {
		t.Errorf("above limit: prefix should be first 70%% of limit")
	}
	// Last 30 chars of limit should be from end
	if !strings.HasSuffix(got, content[len(content)-30:]) {
		t.Errorf("above limit: suffix should be last 30%% of limit")
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	got, name := Truncate("", 100)
	if got != "" {
		t.Errorf("empty input: expected empty output, got %q", got)
	}
	if name != "none" {
		t.Errorf("empty input: name = %q, want none", name)
	}
}

func TestTruncate_ZeroLimit(t *testing.T) {
	content := "some content"
	got, name := Truncate(content, 0)
	if got != content {
		t.Errorf("zero limit: content should be unchanged")
	}
	if name != "none" {
		t.Errorf("zero limit: name = %q, want none", name)
	}
}

// ---------------------------------------------------------------------------
// Task 8.3 — git.go formatters
// ---------------------------------------------------------------------------

func TestFormatStatus_Clean(t *testing.T) {
	content := `On branch main
nothing to commit, working tree clean`
	got := FormatStatus(content)
	if !strings.Contains(got, "nothing to commit") {
		t.Errorf("clean status: should retain 'nothing to commit'")
	}
}

func TestFormatStatus_MixedChanges(t *testing.T) {
	content := `On branch main
Changes to be committed:
  (use "git restore --staged <file>..." to unstage)
	new file:   foo.go

Changes not staged for commit:
  (use "git add <file>..." to update what will be committed)
	modified:   bar.go`

	got := FormatStatus(content)
	if strings.Contains(got, `use "git`) {
		t.Errorf("status with changes: hint lines should be stripped")
	}
	if !strings.Contains(got, "modified:") {
		t.Errorf("status with changes: file state lines should be preserved")
	}
	if !strings.Contains(got, "new file:") {
		t.Errorf("status with changes: staged file lines should be preserved")
	}
	if len(got) >= len(content) {
		t.Errorf("status with changes: output should be shorter than input")
	}
}

func TestFormatDiff_Normal(t *testing.T) {
	content := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,5 +1,5 @@
 unchanged line
-removed line
+added line
 another unchanged`

	got := FormatDiff(content)
	if strings.Contains(got, " unchanged line") {
		t.Errorf("diff: context lines should be removed")
	}
	if !strings.Contains(got, "-removed line") {
		t.Errorf("diff: removed lines must be kept")
	}
	if !strings.Contains(got, "+added line") {
		t.Errorf("diff: added lines must be kept")
	}
	if !strings.Contains(got, "@@") {
		t.Errorf("diff: hunk headers must be kept")
	}
}

func TestFormatDiff_Empty(t *testing.T) {
	got := FormatDiff("")
	if got != "" {
		t.Errorf("empty diff: expected empty output, got %q", got)
	}
}

func TestFormatDiff_BinaryFile(t *testing.T) {
	content := `diff --git a/image.png b/image.png
index abc..def 100644
Binary files a/image.png and b/image.png differ`

	got := FormatDiff(content)
	if !strings.Contains(got, "Binary files") {
		t.Errorf("binary diff: binary file indicator should be preserved")
	}
}

func TestFormatLog_Standard(t *testing.T) {
	var commits []string
	for i := 0; i < 5; i++ {
		commits = append(commits, `commit abc1234`+"\nAuthor: Alice <a@b.com>\nDate:   Mon Jan 1 00:00:00 2026\n\n    Commit message\n    More details\n")
	}
	content := strings.Join(commits, "\n")
	got := FormatLog(content)

	lines := strings.Split(strings.TrimSpace(got), "\n")
	commitCount := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "commit ") {
			commitCount++
		}
	}
	if commitCount != 5 {
		t.Errorf("git log: expected 5 commit lines, got %d", commitCount)
	}
}

func TestFormatLog_Empty(t *testing.T) {
	got := FormatLog("")
	if strings.TrimSpace(got) != "" {
		t.Errorf("empty log: expected empty output, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Task 8.4 — listing.go
// ---------------------------------------------------------------------------

func TestFormatListing_LsLa(t *testing.T) {
	content := `total 48
drwxr-xr-x 5 user group 4096 Mar 20 10:00 .
drwxr-xr-x 10 user group 4096 Mar 20 09:00 ..
drwxr-xr-x 3 user group 4096 Mar 20 10:00 internal
-rw-r--r-- 1 user group 512 Mar 20 10:00 README.md
-rw-r--r-- 1 user group 1024 Mar 20 10:00 main.go`

	got, name := FormatListing(content)
	if !strings.Contains(got, "dirs,") {
		t.Errorf("ls -la: summary line expected")
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("ls -la: file name should be present")
	}
	if name != "listing" {
		t.Errorf("ls -la: name = %q, want listing", name)
	}
}

func TestFormatListing_FindOutput(t *testing.T) {
	var paths []string
	for i := 0; i < 10; i++ {
		paths = append(paths, "internal/pkg/file.go")
	}
	content := strings.Join(paths, "\n")
	got, _ := FormatListing(content)
	if !strings.Contains(got, "dirs,") {
		t.Errorf("find output: summary line expected")
	}
}

// ---------------------------------------------------------------------------
// Task 8.5 — file.go FilterFileContent
// ---------------------------------------------------------------------------

func TestFilterFileContent_DataFormats_AllPassThrough(t *testing.T) {
	dataExts := []string{".json", ".yaml", ".yml", ".toml", ".xml", ".csv", ".md", ".sql"}
	content := `{"key": "value"}
# comment
some: yaml`

	for _, ext := range dataExts {
		t.Run(ext, func(t *testing.T) {
			got, name := FilterFileContent("file"+ext, content, "aggressive")
			if got != content {
				t.Errorf("%s: data format should pass through unchanged", ext)
			}
			if name != "none" {
				t.Errorf("%s: name = %q, want none", ext, name)
			}
		})
	}
}

func TestFilterFileContent_UnknownExt_Passthrough(t *testing.T) {
	content := "some content"
	got, name := FilterFileContent("file.xyz", content, "aggressive")
	if got != content {
		t.Errorf("unknown ext: should pass through unchanged")
	}
	if name != "none" {
		t.Errorf("unknown ext: name = %q, want none", name)
	}
}

func TestFilterFileContent_GoMinimal_CommentsStripped(t *testing.T) {
	content := "package foo\n\n// This comment should be removed\nfunc Foo() {}\n"
	got, name := FilterFileContent("foo.go", content, "minimal")
	if strings.Contains(got, "// This comment") {
		t.Errorf("minimal: single-line comments should be stripped")
	}
	if !strings.Contains(got, "func Foo") {
		t.Errorf("minimal: function signatures should be retained")
	}
	if name != "file_minimal" {
		t.Errorf("minimal: name = %q, want file_minimal", name)
	}
}

func TestFilterFileContent_GoAggressive(t *testing.T) {
	content := `package foo

import "fmt"

// Comment
func Hello() string {
	fmt.Println("hello")
	return "hello"
}

type MyStruct struct {
	Field string
}`

	got, name := FilterFileContent("foo.go", content, "aggressive")
	if !strings.Contains(got, "func Hello") {
		t.Errorf("aggressive: function signatures should be kept")
	}
	if !strings.Contains(got, "type MyStruct") {
		t.Errorf("aggressive: type declarations should be kept")
	}
	if name != "file_aggressive" {
		t.Errorf("aggressive: name = %q, want file_aggressive", name)
	}
	if len(got) >= len(content) {
		t.Errorf("aggressive: output should be shorter than input")
	}
}

func TestFilterFileContent_LevelNo_Unchanged(t *testing.T) {
	content := "package foo\n// comment\nfunc Foo() {}\n"
	got, name := FilterFileContent("foo.go", content, "no")
	if got != content {
		t.Errorf("level no: content should be unchanged")
	}
	if name != "none" {
		t.Errorf("level no: name = %q, want none", name)
	}
}

// ---------------------------------------------------------------------------
// Task 8.6 — http.go FilterHTTP
// ---------------------------------------------------------------------------

func TestFilterHTTP_HTMLTagsStripped(t *testing.T) {
	content := `<!DOCTYPE html>
<html><head><title>Test</title></head>
<body><h1>Hello</h1><p>World</p></body></html>`

	got, name := FilterHTTP(content, 8000)
	if strings.Contains(got, "<html>") || strings.Contains(got, "<p>") {
		t.Errorf("HTML: tags should be stripped")
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Errorf("HTML: visible text should be preserved")
	}
	if name != "http_html" {
		t.Errorf("HTML: name = %q, want http_html", name)
	}
}

func TestFilterHTTP_JSON_NoTagStrip(t *testing.T) {
	content := `{"status": "ok", "data": [1, 2, 3]}`
	got, name := FilterHTTP(content, 8000)
	if got != content {
		t.Errorf("JSON: non-HTML should pass through unchanged (below limit)")
	}
	if name != "none" {
		t.Errorf("JSON: name = %q, want none", name)
	}
}

func TestFilterHTTP_DocTypeDetection(t *testing.T) {
	// <!DOCTYPE in first 512 bytes should be detected as HTML
	content := `<!DOCTYPE html><html><body>text</body></html>`
	got, name := FilterHTTP(content, 8000)
	if strings.Contains(got, "<!DOCTYPE") {
		t.Errorf("HTML doctype: should be stripped")
	}
	if name != "http_html" {
		t.Errorf("HTML doctype: name = %q, want http_html", name)
	}
}

func TestFilterHTTP_Truncation(t *testing.T) {
	// Non-HTML content that exceeds limit should be truncated
	content := strings.Repeat("a", 500)
	got, name := FilterHTTP(content, 100)
	if !strings.Contains(got, "chars omitted") {
		t.Errorf("truncation: marker expected for large non-HTML content")
	}
	if name != "generic_truncate" {
		t.Errorf("truncation: name = %q, want generic_truncate", name)
	}
}

// ---------------------------------------------------------------------------
// Task 8.10 coverage check is done via go test -cover (see Phase 9)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Native Context-Mode: PreApply tests
// ---------------------------------------------------------------------------

func TestPreApply_ContextModeOff_ReturnsFalse(t *testing.T) {
	// Context mode is "off" - PreApply should return false (continue execution)
	cfg := config.ContextModeConfig{
		Mode: config.ContextModeOff,
	}
	input := json.RawMessage(`{"command": "echo hello"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if shouldSkip {
		t.Errorf("PreApply with Mode=off returned shouldSkip=true, want false")
	}
	if result.Content != "" {
		t.Errorf("PreApply with Mode=off returned non-empty content: %q", result.Content)
	}
}

func TestPreApply_ShellToolWithMaxOutputHint(t *testing.T) {
	// When context mode is "auto", PreApply intercepts shell_exec and runs via sandbox
	cfg := config.ContextModeConfig{
		Mode:             config.ContextModeAuto,
		ShellMaxOutput:   4096,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
	}
	input := json.RawMessage(`{"command": "echo hello"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if !shouldSkip {
		t.Errorf("PreApply should intercept shell_exec in auto mode, got shouldSkip=false")
	}
	if result.Content == "" {
		t.Errorf("PreApply returned empty content")
	}
	if strings.TrimSpace(result.Content) != "hello" {
		t.Errorf("PreApply returned %q, want %q", result.Content, "hello")
	}
}

func TestPreApply_FileReadToolWithChunkSize(t *testing.T) {
	// When context mode is "auto", PreApply should handle file read tool
	cfg := config.ContextModeConfig{
		Mode:          config.ContextModeAuto,
		FileChunkSize: 2000,
	}
	input := json.RawMessage(`{"path": "/tmp/test.txt"}`)

	result, shouldSkip := PreApply(context.Background(), "read_file", input, cfg)

	// For Phase 2, PreApply doesn't actually intercept yet - returns false
	if shouldSkip {
		t.Errorf("Phase 2: PreApply should return false (not yet implemented), got true")
	}
	_ = result // Mark as used for now
}

func TestPreApply_UnsupportedToolReturnsFalse(t *testing.T) {
	// For tools not supported by PreApply, should return false
	cfg := config.ContextModeConfig{
		Mode: config.ContextModeAuto,
	}
	input := json.RawMessage(`{}`)

	result, shouldSkip := PreApply(context.Background(), "unknown_tool", input, cfg)

	if shouldSkip {
		t.Errorf("PreApply for unknown tool returned shouldSkip=true, want false")
	}
	if result.Content != "" {
		t.Errorf("PreApply for unknown tool returned non-empty content: %q", result.Content)
	}
}

func TestPreApply_ConservativeModeReturnsTrue(t *testing.T) {
	// Conservative mode should intercept shell_exec via sandbox
	cfg := config.ContextModeConfig{
		Mode:             config.ContextModeConservative,
		ShellMaxOutput:   8192,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
	}
	input := json.RawMessage(`{"command": "echo test"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if !shouldSkip {
		t.Errorf("PreApply with conservative mode should intercept, got shouldSkip=false")
	}
	if strings.TrimSpace(result.Content) != "test" {
		t.Errorf("PreApply returned %q, want %q", result.Content, "test")
	}
}

func TestPreApply_AutoModeIntercepts(t *testing.T) {
	// Auto mode should intercept shell_exec via sandbox
	cfg := config.ContextModeConfig{
		Mode:             config.ContextModeAuto,
		ShellMaxOutput:   4096,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
	}
	input := json.RawMessage(`{"command": "echo test"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if !shouldSkip {
		t.Errorf("PreApply with auto mode should intercept, got shouldSkip=false")
	}
	if strings.TrimSpace(result.Content) != "test" {
		t.Errorf("PreApply returned %q, want %q", result.Content, "test")
	}
}

// Task 2.3: Tests for shell tool PreExecute behavior
func TestPreApply_ShellTool_AutoMode_ExecutesCommand(t *testing.T) {
	cfg := config.ContextModeConfig{
		Mode:             config.ContextModeAuto,
		ShellMaxOutput:   4096,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
	}
	input := json.RawMessage(`{"command": "echo hello world"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if !shouldSkip {
		t.Errorf("PreApply for shell_exec in auto mode should intercept (Phase 3)")
	}
	if strings.TrimSpace(result.Content) != "hello world" {
		t.Errorf("PreApply returned %q, want %q", result.Content, "hello world")
	}
}

func TestPreApply_ShellTool_ConservativeMode_HigherLimit(t *testing.T) {
	cfg := config.ContextModeConfig{
		Mode:             config.ContextModeConservative,
		ShellMaxOutput:   8192,
		SandboxTimeout:   30 * time.Second,
		SandboxKeepFirst: 20,
		SandboxKeepLast:  10,
	}
	input := json.RawMessage(`{"command": "echo found"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if !shouldSkip {
		t.Errorf("PreApply for shell_exec in conservative mode should intercept")
	}
	if strings.TrimSpace(result.Content) != "found" {
		t.Errorf("PreApply returned %q, want %q", result.Content, "found")
	}
}

func TestPreApply_ShellTool_InvalidJSON_ReturnsFalse(t *testing.T) {
	cfg := config.ContextModeConfig{
		Mode: config.ContextModeAuto,
	}
	// Invalid JSON - missing command field
	input := json.RawMessage(`{"not_command": "test"}`)

	result, shouldSkip := PreApply(context.Background(), "shell_exec", input, cfg)

	if shouldSkip {
		t.Errorf("PreApply with invalid JSON should return false, got true")
	}
	if result.Content != "" {
		t.Errorf("PreApply with invalid JSON returned non-empty content: %q", result.Content)
	}
}
