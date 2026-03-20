package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillMDContent returns a minimal valid skill markdown for tests.
func skillMDContent(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: \"%s\"\n---\n\nProse content.\n", name, description)
}

// writeSkillsTempConfig writes a minimal YAML config file with skills_dir set.
// skillPaths may be empty. Returns the config file path.
func writeSkillsTempConfig(t *testing.T, storeDir string, skillPaths []string, registryURL string) string {
	t.Helper()
	dir := t.TempDir()

	var sb strings.Builder
	sb.WriteString("provider:\n  type: anthropic\n  model: claude-3-sonnet-20240229\n  api_key: test-key\n")
	sb.WriteString(fmt.Sprintf("skills_dir: %s\n", storeDir))
	if registryURL != "" {
		sb.WriteString(fmt.Sprintf("skills_registry_url: %s\n", registryURL))
	}
	if len(skillPaths) > 0 {
		sb.WriteString("skills:\n")
		for _, p := range skillPaths {
			sb.WriteString(fmt.Sprintf("  - %s\n", p))
		}
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// ---------------------------------------------------------------------------
// skillsAdd tests
// ---------------------------------------------------------------------------

func TestSkillsAdd_URL_HappyPath(t *testing.T) {
	content := skillMDContent("url-skill", "A skill from URL")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		fmt.Fprint(w, content)
	}))
	defer srv.Close()

	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	err := skillsAdd([]string{srv.URL + "/url-skill.md"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was written to store dir.
	entries, _ := os.ReadDir(storeDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in store dir, got %d", len(entries))
	}
}

func TestSkillsAdd_Force(t *testing.T) {
	content := skillMDContent("force-skill", "Force test skill")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, content)
	}))
	defer srv.Close()

	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	// First install should succeed.
	if err := skillsAdd([]string{srv.URL + "/force-skill.md"}, cfgPath); err != nil {
		t.Fatalf("first install: unexpected error: %v", err)
	}

	// Second install without --force should fail.
	if err := skillsAdd([]string{srv.URL + "/force-skill.md"}, cfgPath); err == nil {
		t.Fatal("expected error on second install without --force, got nil")
	}

	// Second install with --force should succeed.
	if err := skillsAdd([]string{"--force", srv.URL + "/force-skill.md"}, cfgPath); err != nil {
		t.Fatalf("--force install: unexpected error: %v", err)
	}
}

func TestSkillsAdd_LocalPath(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()

	// Write a local skill file.
	localPath := filepath.Join(dir, "local-skill.md")
	if err := os.WriteFile(localPath, []byte(skillMDContent("local-skill", "Local skill")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	err := skillsAdd([]string{localPath}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, _ := os.ReadDir(storeDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in store dir, got %d", len(entries))
	}
}

func TestSkillsAdd_ShortName_NoRegistry(t *testing.T) {
	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "") // no registry URL

	err := skillsAdd([]string{"some-skill"}, cfgPath)
	if err == nil {
		t.Fatal("expected error for short name without registry, got nil")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("expected error message to mention registry, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// skillsList tests
// ---------------------------------------------------------------------------

func TestSkillsList_Empty(t *testing.T) {
	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	err := skillsList([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Output is printed to stdout; just verify no error.
}

func TestSkillsList_WithSkills(t *testing.T) {
	storeDir := t.TempDir()

	// Write two skill files in the store dir.
	path1 := filepath.Join(storeDir, "alpha.md")
	path2 := filepath.Join(storeDir, "beta.md")
	if err := os.WriteFile(path1, []byte(skillMDContent("alpha", "Alpha skill")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path2, []byte(skillMDContent("beta", "Beta skill")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeSkillsTempConfig(t, storeDir, []string{path1, path2}, "")

	err := skillsList([]string{}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// skillsRemove tests
// ---------------------------------------------------------------------------

func TestSkillsRemove_HappyPath(t *testing.T) {
	storeDir := t.TempDir()
	skillPath := filepath.Join(storeDir, "remove-me.md")
	if err := os.WriteFile(skillPath, []byte(skillMDContent("remove-me", "To be removed")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeSkillsTempConfig(t, storeDir, []string{skillPath}, "")

	err := skillsRemove([]string{"--yes", "remove-me"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should be deleted (it's in storeDir).
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("expected skill file to be deleted, but it still exists")
	}
}

func TestSkillsRemove_KeepFile(t *testing.T) {
	storeDir := t.TempDir()
	skillPath := filepath.Join(storeDir, "keep-me.md")
	if err := os.WriteFile(skillPath, []byte(skillMDContent("keep-me", "Keep file")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeSkillsTempConfig(t, storeDir, []string{skillPath}, "")

	err := skillsRemove([]string{"--yes", "--keep-file", "keep-me"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// File should still exist.
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		t.Error("expected skill file to be preserved with --keep-file, but it was deleted")
	}
}

func TestSkillsRemove_NotFound(t *testing.T) {
	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	err := skillsRemove([]string{"--yes", "nonexistent"}, cfgPath)
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}

func TestSkillsRemove_NoYesNonTTY(t *testing.T) {
	storeDir := t.TempDir()
	skillPath := filepath.Join(storeDir, "test-skill.md")
	if err := os.WriteFile(skillPath, []byte(skillMDContent("test-skill", "Test")), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeSkillsTempConfig(t, storeDir, []string{skillPath}, "")

	// stdin in tests is not a TTY, so omitting --yes should produce an error.
	err := skillsRemove([]string{"test-skill"}, cfgPath)
	if err == nil {
		t.Fatal("expected error when --yes not provided on non-TTY stdin, got nil")
	}
}

// ---------------------------------------------------------------------------
// skillsInfo tests
// ---------------------------------------------------------------------------

func TestSkillsInfo_HappyPath(t *testing.T) {
	storeDir := t.TempDir()
	content := "---\nname: info-skill\ndescription: \"Info skill\"\nversion: \"1.0\"\nauthor: \"Test Author\"\n---\n\nProse content.\n"
	skillPath := filepath.Join(storeDir, "info-skill.md")
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgPath := writeSkillsTempConfig(t, storeDir, []string{skillPath}, "")

	err := skillsInfo([]string{"info-skill"}, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSkillsInfo_NotFound(t *testing.T) {
	storeDir := t.TempDir()
	cfgPath := writeSkillsTempConfig(t, storeDir, nil, "")

	err := skillsInfo([]string{"nonexistent"}, cfgPath)
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}
