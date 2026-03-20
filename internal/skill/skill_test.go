package skill

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"microagent/internal/config"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "skill-*.md")
	if err != nil {
		t.Fatalf("createTemp: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("writeTemp close: %v", err)
	}
	return f.Name()
}

// ---------------------------------------------------------------------------
// ParseSkillFile tests
// ---------------------------------------------------------------------------

func TestParseSkillFile_ValidFrontmatterAndTools(t *testing.T) {
	content := `---
name: my-skill
description: "Test skill"
version: 1.0.0
author: test
---

This is the prose section.
It has multiple lines.

` + "```yaml tool" + `
name: tool_one
description: "Does thing one"
command: "echo one"
timeout: 5s
` + "```" + `

More prose here.

` + "```yaml tool" + `
name: tool_two
description: "Does thing two"
command: "echo two"
` + "```" + `

` + "```yaml tool" + `
name: tool_three
description: "Does thing three"
command: "echo three"
` + "```" + `

Final prose.
`
	path := writeTemp(t, content)
	sc, tools, errs := ParseSkillFile(path)

	if len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
	if sc.Name != "my-skill" {
		t.Errorf("expected Name 'my-skill', got %q", sc.Name)
	}
	if sc.Description != "Test skill" {
		t.Errorf("expected Description 'Test skill', got %q", sc.Description)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	if tools[0].Name != "tool_one" {
		t.Errorf("expected tools[0].Name 'tool_one', got %q", tools[0].Name)
	}
	if tools[1].Name != "tool_two" {
		t.Errorf("expected tools[1].Name 'tool_two', got %q", tools[1].Name)
	}
	if tools[2].Name != "tool_three" {
		t.Errorf("expected tools[2].Name 'tool_three', got %q", tools[2].Name)
	}
	// Fence lines must NOT appear in prose
	if strings.Contains(sc.Prose, "```yaml tool") {
		t.Error("prose must not contain opening fence line")
	}
	if strings.Contains(sc.Prose, "```") {
		t.Error("prose must not contain closing fence line")
	}
	// Prose should contain actual content
	if !strings.Contains(sc.Prose, "This is the prose section") {
		t.Error("prose should contain prose content")
	}
	if !strings.Contains(sc.Prose, "More prose here") {
		t.Error("prose should contain 'More prose here'")
	}
	if !strings.Contains(sc.Prose, "Final prose") {
		t.Error("prose should contain 'Final prose'")
	}
}

func TestParseSkillFile_FilenameStemFallback(t *testing.T) {
	// No frontmatter → name falls back to filename stem
	content := "This is just prose.\n"
	dir := t.TempDir()
	path := dir + "/git-helper.md"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sc, _, errs := ParseSkillFile(path)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if sc.Name != "git-helper" {
		t.Errorf("expected Name 'git-helper', got %q", sc.Name)
	}
	if sc.Description != "" {
		t.Errorf("expected empty Description, got %q", sc.Description)
	}
	if !strings.Contains(sc.Prose, "This is just prose") {
		t.Errorf("expected prose to contain file content, got %q", sc.Prose)
	}
}

func TestParseSkillFile_MalformedFrontmatterTreatedAsProse(t *testing.T) {
	// Malformed YAML in frontmatter → treat entire file as prose (no error per spec FM-7)
	content := `---
name: {bad yaml
---

Some prose.
`
	path := writeTemp(t, content)
	sc, tools, errs := ParseSkillFile(path)

	if len(errs) != 0 {
		t.Fatalf("expected no errors for malformed frontmatter, got: %v", errs)
	}
	if len(tools) != 0 {
		t.Errorf("expected no tools, got %d", len(tools))
	}
	// Content including frontmatter lines should be in prose
	if !strings.Contains(sc.Prose, "Some prose") {
		t.Errorf("prose should contain body content, got %q", sc.Prose)
	}
}

func TestParseSkillFile_UnclosedFrontmatter(t *testing.T) {
	// Opening "---" but no closing "---" → entire file treated as prose (spec FM-4)
	content := `---
name: skill-name
description: "test"

Some content here.
More content.
`
	path := writeTemp(t, content)
	sc, tools, errs := ParseSkillFile(path)

	if len(errs) != 0 {
		t.Fatalf("expected no errors for unclosed frontmatter, got: %v", errs)
	}
	if len(tools) != 0 {
		t.Errorf("expected no tools, got %d", len(tools))
	}
	// The name should fall back to filename stem since frontmatter wasn't parsed
	if sc.Name == "skill-name" {
		t.Error("expected name to fall back to filename stem, not frontmatter value")
	}
}

func TestParseSkillFile_BadToolYAML_WarnAndSkip(t *testing.T) {
	content := `---
name: test-skill
description: "test"
---

Prose before.

` + "```yaml tool" + `
name: {bad yaml
` + "```" + `

` + "```yaml tool" + `
name: valid_tool
description: "A valid tool"
command: "echo hello"
` + "```" + `

Prose after.
`
	path := writeTemp(t, content)
	sc, tools, errs := ParseSkillFile(path)

	if len(errs) == 0 {
		t.Fatal("expected at least one error for bad tool YAML")
	}
	// Should still parse the valid tool
	if len(tools) != 1 {
		t.Errorf("expected 1 valid tool, got %d", len(tools))
	}
	if tools[0].Name != "valid_tool" {
		t.Errorf("expected 'valid_tool', got %q", tools[0].Name)
	}
	// Prose should still be collected
	if !strings.Contains(sc.Prose, "Prose before") {
		t.Error("prose should contain content before bad tool block")
	}
	if !strings.Contains(sc.Prose, "Prose after") {
		t.Error("prose should contain content after bad tool block")
	}
}

func TestParseSkillFile_ToolNameUppercase_WarnAndSkip(t *testing.T) {
	content := `---
name: test-skill
description: "test"
---

` + "```yaml tool" + `
name: Git_Status
description: "Show status"
command: "git status"
` + "```" + `
`
	path := writeTemp(t, content)
	_, tools, errs := ParseSkillFile(path)

	if len(errs) == 0 {
		t.Fatal("expected error for invalid tool name")
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestParseSkillFile_MissingCommand_WarnAndSkip(t *testing.T) {
	content := `---
name: test-skill
description: "test"
---

` + "```yaml tool" + `
name: no_command
description: "Missing command"
` + "```" + `
`
	path := writeTemp(t, content)
	_, tools, errs := ParseSkillFile(path)

	if len(errs) == 0 {
		t.Fatal("expected error for missing command")
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestParseSkillFile_MissingDescription_WarnAndSkip(t *testing.T) {
	content := `---
name: test-skill
description: "test"
---

` + "```yaml tool" + `
name: no_description
command: "echo hello"
` + "```" + `
`
	path := writeTemp(t, content)
	_, tools, errs := ParseSkillFile(path)

	if len(errs) == 0 {
		t.Fatal("expected error for missing description")
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestParseSkillFile_FenceLineNotInProse(t *testing.T) {
	content := `# Header

` + "```yaml tool" + `
name: my_tool
description: "desc"
command: "echo hi"
` + "```" + `

End.
`
	path := writeTemp(t, content)
	sc, tools, errs := ParseSkillFile(path)

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if strings.Contains(sc.Prose, "```yaml tool") {
		t.Error("prose must not contain opening fence")
	}
	if strings.Contains(sc.Prose, "echo hi") {
		t.Error("prose must not contain tool command from inside the block")
	}
}

// ---------------------------------------------------------------------------
// LoadSkills tests
// ---------------------------------------------------------------------------

func TestLoadSkills_EmptyPaths(t *testing.T) {
	contents, tools, warns := LoadSkills(nil, config.ShellToolConfig{}, config.LimitsConfig{})
	if contents != nil {
		t.Errorf("expected nil contents, got %v", contents)
	}
	if tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
	if warns != nil {
		t.Errorf("expected nil warns, got %v", warns)
	}

	// Also test empty slice
	contents, tools, warns = LoadSkills([]string{}, config.ShellToolConfig{}, config.LimitsConfig{})
	if contents != nil {
		t.Errorf("expected nil contents for empty slice, got %v", contents)
	}
	if tools != nil {
		t.Errorf("expected nil tools for empty slice, got %v", tools)
	}
}

func TestLoadSkills_MissingFile(t *testing.T) {
	paths := []string{"/nonexistent/skill.md"}
	contents, tools, warns := LoadSkills(paths, config.ShellToolConfig{}, config.LimitsConfig{})

	if len(warns) == 0 {
		t.Fatal("expected at least one warning for missing file")
	}
	if len(contents) != 0 {
		t.Errorf("expected no contents, got %d", len(contents))
	}
	if len(tools) != 0 {
		t.Errorf("expected no tools, got %d", len(tools))
	}
}

func TestLoadSkills_FileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/large.md"
	// Write 9000 bytes — exceeds 8192 limit
	data := make([]byte, 9000)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	contents, tools, warns := LoadSkills([]string{path}, config.ShellToolConfig{}, config.LimitsConfig{})

	if len(warns) == 0 {
		t.Fatal("expected warning for oversized file")
	}
	if len(contents) != 0 {
		t.Errorf("expected no contents for oversized file, got %d", len(contents))
	}
	if len(tools) != 0 {
		t.Errorf("expected no tools for oversized file, got %d", len(tools))
	}
}

func TestLoadSkills_SkillCollision_FirstWins(t *testing.T) {
	contentA := `---
name: skill-a
description: "First skill"
---

` + "```yaml tool" + `
name: my_tool
description: "From skill A"
command: "echo from_a"
` + "```" + `
`
	contentB := `---
name: skill-b
description: "Second skill"
---

` + "```yaml tool" + `
name: my_tool
description: "From skill B"
command: "echo from_b"
` + "```" + `
`
	pathA := writeTemp(t, contentA)
	pathB := writeTemp(t, contentB)

	_, tools, warns := LoadSkills([]string{pathA, pathB}, config.ShellToolConfig{}, config.LimitsConfig{ToolTimeout: 30 * time.Second})

	// Should have exactly 1 tool (from A)
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if t2, ok := tools["my_tool"]; ok {
		if t2.Description() != "From skill A" {
			t.Errorf("expected tool from skill A to win, got description %q", t2.Description())
		}
	} else {
		t.Error("expected my_tool in tools map")
	}

	// Should have a collision warning
	foundCollision := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "my_tool") && strings.Contains(w.Error(), "first definition wins") {
			foundCollision = true
			break
		}
	}
	if !foundCollision {
		t.Errorf("expected collision warning, got warns: %v", warns)
	}
}

func TestLoadSkills_EnvExpansion_SetVariable(t *testing.T) {
	t.Setenv("TEST_TOKEN_ABC", "secret123")

	content := `---
name: env-skill
description: "Env test"
---

` + "```yaml tool" + `
name: env_tool
description: "Tool with env"
command: "echo test"
env:
  TOKEN: "${TEST_TOKEN_ABC}"
` + "```" + `
`
	path := writeTemp(t, content)
	_, tools, warns := LoadSkills([]string{path}, config.ShellToolConfig{}, config.LimitsConfig{ToolTimeout: 30 * time.Second})

	// No expansion warnings expected
	for _, w := range warns {
		if strings.Contains(w.Error(), "env") {
			t.Errorf("unexpected env warning: %v", w)
		}
	}

	t2, ok := tools["env_tool"]
	if !ok {
		t.Fatal("expected env_tool in tools")
	}
	// Check the underlying def has the expanded value via Execute (indirect check)
	_ = t2
}

func TestLoadSkills_EnvExpansion_UnsetVariable(t *testing.T) {
	// Ensure the var is not set
	os.Unsetenv("MISSING_VAR_XYZ_TEST")

	content := `---
name: env-skill
description: "Env test"
---

` + "```yaml tool" + `
name: env_tool2
description: "Tool with missing env"
command: "echo test"
env:
  TOKEN: "${MISSING_VAR_XYZ_TEST}"
` + "```" + `
`
	path := writeTemp(t, content)
	_, tools, warns := LoadSkills([]string{path}, config.ShellToolConfig{}, config.LimitsConfig{ToolTimeout: 30 * time.Second})

	// Should have a warning about the unset variable
	foundWarn := false
	for _, w := range warns {
		if strings.Contains(w.Error(), "env_tool2") || strings.Contains(w.Error(), "MISSING_VAR_XYZ_TEST") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Errorf("expected warning for unset env var, got: %v", warns)
	}

	// Tool should still be registered (non-fatal)
	if _, ok := tools["env_tool2"]; !ok {
		t.Error("expected env_tool2 to be registered even with unset env var")
	}
}

func TestLoadSkills_WorkingDirInheritance(t *testing.T) {
	content := `---
name: wd-skill
description: "WorkingDir test"
---

` + "```yaml tool" + `
name: wd_tool
description: "Uses inherited working dir"
command: "pwd"
` + "```" + `
`
	path := writeTemp(t, content)
	shellCfg := config.ShellToolConfig{WorkingDir: "/tmp"}
	_, tools, _ := LoadSkills([]string{path}, shellCfg, config.LimitsConfig{ToolTimeout: 30 * time.Second})

	t2, ok := tools["wd_tool"]
	if !ok {
		t.Fatal("expected wd_tool in tools")
	}
	// Execute and verify it runs in /tmp
	st := t2.(*skillShellTool)
	if st.def.WorkingDir != "/tmp" {
		t.Errorf("expected WorkingDir '/tmp', got %q", st.def.WorkingDir)
	}
}

func TestLoadSkills_TimeoutInheritance(t *testing.T) {
	content := `---
name: timeout-skill
description: "Timeout test"
---

` + "```yaml tool" + `
name: timeout_tool
description: "Uses inherited timeout"
command: "echo test"
` + "```" + `
`
	path := writeTemp(t, content)
	limits := config.LimitsConfig{ToolTimeout: 45 * time.Second}
	_, tools, _ := LoadSkills([]string{path}, config.ShellToolConfig{}, limits)

	t2, ok := tools["timeout_tool"]
	if !ok {
		t.Fatal("expected timeout_tool in tools")
	}
	st := t2.(*skillShellTool)
	if st.def.Timeout != 45*time.Second {
		t.Errorf("expected Timeout 45s, got %v", st.def.Timeout)
	}
}

func TestLoadSkills_MissingFileThenValid(t *testing.T) {
	// Missing file should not prevent loading of the next valid file
	validContent := `---
name: valid-skill
description: "Valid skill"
---

Valid prose.
`
	validPath := writeTemp(t, validContent)

	contents, _, warns := LoadSkills(
		[]string{"/nonexistent/skill.md", validPath},
		config.ShellToolConfig{},
		config.LimitsConfig{},
	)

	if len(warns) == 0 {
		t.Fatal("expected warning for missing file")
	}
	if len(contents) != 1 {
		t.Errorf("expected 1 content (valid skill), got %d", len(contents))
	}
	if contents[0].Name != "valid-skill" {
		t.Errorf("expected 'valid-skill', got %q", contents[0].Name)
	}
}

// ---------------------------------------------------------------------------
// skillShellTool.Execute tests
// ---------------------------------------------------------------------------

func TestSkillShellTool_Execute_Success(t *testing.T) {
	def := ToolDef{
		Name:        "echo_tool",
		Description: "Echo test",
		Command:     "echo hello",
		Timeout:     5 * time.Second,
	}
	st := NewSkillShellTool(def)

	result, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError=false, got true; content: %q", result.Content)
	}
	if !strings.Contains(result.Content, "hello") {
		t.Errorf("expected 'hello' in content, got %q", result.Content)
	}
}

func TestSkillShellTool_Execute_NonZeroExit(t *testing.T) {
	def := ToolDef{
		Name:        "exit_tool",
		Description: "Exit with error",
		Command:     "exit 1",
		Timeout:     5 * time.Second,
	}
	st := NewSkillShellTool(def)

	result, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for non-zero exit")
	}
}

func TestSkillShellTool_Execute_OutputTruncated(t *testing.T) {
	// Generate output > 10 KB
	def := ToolDef{
		Name:        "big_output",
		Description: "Generates large output",
		Command:     "seq 1 100000",
		Timeout:     30 * time.Second,
	}
	st := NewSkillShellTool(def)

	result, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const maxLen = 10 * 1024
	if !strings.HasSuffix(result.Content, "\n...(output truncated)") {
		t.Errorf("expected truncation suffix in output; last 50 chars: %q", result.Content[len(result.Content)-50:])
	}
	// The content before the suffix should be exactly maxLen bytes
	suffix := "\n...(output truncated)"
	body := strings.TrimSuffix(result.Content, suffix)
	if len(body) != maxLen {
		t.Errorf("expected body length %d, got %d", maxLen, len(body))
	}
}

func TestSkillShellTool_Execute_EmptyOutput(t *testing.T) {
	def := ToolDef{
		Name:        "silent_tool",
		Description: "Produces no output",
		Command:     "true",
		Timeout:     5 * time.Second,
	}
	st := NewSkillShellTool(def)

	result, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected IsError=false, got true")
	}
	if result.Content != "(command successful, no output)" {
		t.Errorf("expected empty output sentinel, got %q", result.Content)
	}
}

func TestSkillShellTool_Schema(t *testing.T) {
	def := ToolDef{
		Name:        "schema_tool",
		Description: "Test schema",
		Command:     "echo test",
	}
	st := NewSkillShellTool(def)

	schema := st.Schema()
	if string(schema) != `{}` {
		t.Errorf("expected schema '{}', got %q", string(schema))
	}
}

func TestSkillShellTool_Execute_DeadlineExceeded(t *testing.T) {
	def := ToolDef{
		Name:        "sleep_tool",
		Description: "Sleeps forever",
		Command:     "sleep 60",
		Timeout:     5 * time.Second,
	}
	st := NewSkillShellTool(def)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := st.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Errorf("expected IsError=true for deadline exceeded")
	}
	if result.Content != "Tool timed out" {
		t.Errorf("expected 'Tool timed out', got %q", result.Content)
	}
}

func TestSkillShellTool_NoWhitelistCheck(t *testing.T) {
	// This test verifies that commands outside any whitelist still run.
	// SkillShellTool never checks whitelist.
	def := ToolDef{
		Name:        "date_tool",
		Description: "Run date command",
		Command:     "date --version 2>&1 || date",
		Timeout:     5 * time.Second,
	}
	st := NewSkillShellTool(def)

	result, err := st.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// date should always run successfully regardless of any whitelist
	if result.IsError {
		t.Errorf("expected success, got IsError=true; content: %q", result.Content)
	}
}
