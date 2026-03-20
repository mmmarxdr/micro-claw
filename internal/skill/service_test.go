package skill

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// skillMD returns a minimal valid skill markdown file with the given name and description.
func skillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: \"" + description + "\"\n---\n\nProse content.\n"
}

// writeSkillFile writes a skill .md file to dir with the given filename and content.
func writeSkillFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeSkillFile: %v", err)
	}
	return path
}

// writeTempConfig writes a minimal YAML config file that references the given skill paths.
func writeTempConfig(t *testing.T, dir string, skillPaths []string) string {
	t.Helper()
	content := "provider:\n  type: test\n  api_key: dummy\nskills:\n"
	for _, p := range skillPaths {
		content += "  - " + p + "\n"
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return cfgPath
}

// ---------------------------------------------------------------------------
// SkillService.List tests
// ---------------------------------------------------------------------------

func TestSkillServiceList_TwoRegisteredSkills(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	path1 := writeSkillFile(t, storeDir, "skill-alpha.md", skillMD("alpha", "Alpha skill"))
	path2 := writeSkillFile(t, storeDir, "skill-beta.md", skillMD("beta", "Beta skill"))

	cfgPath := writeTempConfig(t, dir, []string{path1, path2})
	svc := NewSkillService(cfgPath, storeDir, "")

	skills, err := svc.List(false)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Verify names and descriptions.
	nameSet := map[string]InstalledSkill{}
	for _, s := range skills {
		nameSet[s.Name] = s
	}

	alpha, ok := nameSet["alpha"]
	if !ok {
		t.Fatal("expected skill named 'alpha'")
	}
	if alpha.Description != "Alpha skill" {
		t.Errorf("expected description 'Alpha skill', got %q", alpha.Description)
	}
	if !alpha.Managed {
		t.Error("expected alpha to be Managed (path is under storeDir)")
	}

	beta, ok := nameSet["beta"]
	if !ok {
		t.Fatal("expected skill named 'beta'")
	}
	if beta.Description != "Beta skill" {
		t.Errorf("expected description 'Beta skill', got %q", beta.Description)
	}
	if !beta.Managed {
		t.Error("expected beta to be Managed (path is under storeDir)")
	}
}

func TestSkillServiceList_EmptySkills(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")

	cfgPath := writeTempConfig(t, dir, nil)
	svc := NewSkillService(cfgPath, storeDir, "")

	skills, err := svc.List(false)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected empty slice, got %d skills", len(skills))
	}
}

func TestSkillServiceList_ShowStore_OrphanAppears(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Register one skill.
	registered := writeSkillFile(t, storeDir, "registered.md", skillMD("registered", "Registered skill"))
	// Write an orphan — not in config.Skills.
	writeSkillFile(t, storeDir, "orphan.md", skillMD("orphan", "Orphan skill"))

	cfgPath := writeTempConfig(t, dir, []string{registered})
	svc := NewSkillService(cfgPath, storeDir, "")

	skills, err := svc.List(true)
	if err != nil {
		t.Fatalf("List(showStore=true) returned error: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills (1 registered + 1 orphan), got %d", len(skills))
	}

	orphanCount := 0
	for _, s := range skills {
		if s.Orphaned {
			orphanCount++
			if s.Name != "orphan" {
				t.Errorf("expected orphan name 'orphan', got %q", s.Name)
			}
		}
	}
	if orphanCount != 1 {
		t.Errorf("expected 1 orphan, got %d", orphanCount)
	}
}

func TestSkillServiceList_ShowStore_StoreDirAbsent(t *testing.T) {
	dir := t.TempDir()
	// storeDir does NOT exist.
	storeDir := filepath.Join(dir, "nonexistent-store")

	cfgPath := writeTempConfig(t, dir, nil)
	svc := NewSkillService(cfgPath, storeDir, "")

	skills, err := svc.List(true)
	if err != nil {
		t.Fatalf("expected no error when storeDir absent, got: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected empty slice when storeDir absent, got %d", len(skills))
	}
}

// ---------------------------------------------------------------------------
// SkillService.Add tests
// ---------------------------------------------------------------------------

func TestSkillServiceAdd_URLHappyPath(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	content := skillMD("url-skill", "Installed from URL")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, "")
	if err := svc.Add(context.Background(), srv.URL+"/url-skill.md", false); err != nil {
		t.Fatalf("Add returned error: %v", err)
	}

	// Skill file should exist in store.
	destPath := filepath.Join(storeDir, "url-skill.md")
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("expected skill file at %s, got error: %v", destPath, err)
	}

	// Config should reference the skill.
	skills, err := svc.List(false)
	if err != nil {
		t.Fatalf("List after Add: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after add, got %d", len(skills))
	}
	if skills[0].Name != "url-skill" {
		t.Errorf("expected skill name 'url-skill', got %q", skills[0].Name)
	}
}

func TestSkillServiceAdd_ForceOverwrite(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Pre-create the destination file.
	destPath := filepath.Join(storeDir, "my-skill.md")
	os.WriteFile(destPath, []byte(skillMD("my-skill", "Old")), 0o644)

	cfgPath := writeTempConfig(t, dir, []string{destPath})

	newContent := skillMD("my-skill", "Updated via force")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(newContent))
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, "")

	// Without force — should fail.
	if err := svc.Add(context.Background(), srv.URL+"/my-skill.md", false); !errors.Is(err, ErrSkillExists) {
		t.Fatalf("expected ErrSkillExists without force, got: %v", err)
	}

	// With force — should succeed.
	if err := svc.Add(context.Background(), srv.URL+"/my-skill.md", true); err != nil {
		t.Fatalf("Add with force returned error: %v", err)
	}

	data, _ := os.ReadFile(destPath)
	if string(data) != newContent {
		t.Errorf("expected updated content after force overwrite")
	}
}

func TestSkillServiceAdd_CollisionWithoutForce(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	destPath := filepath.Join(storeDir, "existing.md")
	os.WriteFile(destPath, []byte(skillMD("existing", "Existing")), 0o644)

	cfgPath := writeTempConfig(t, dir, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(skillMD("existing", "New")))
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, "")
	err := svc.Add(context.Background(), srv.URL+"/existing.md", false)
	if !errors.Is(err, ErrSkillExists) {
		t.Fatalf("expected ErrSkillExists, got: %v", err)
	}
}

func TestSkillServiceAdd_Non200HTTP(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, "")
	err := svc.Add(context.Background(), srv.URL+"/missing.md", false)
	if err == nil {
		t.Fatal("expected error for non-200 HTTP response")
	}
}

func TestSkillServiceAdd_LocalPathHappyPath(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	// Write a local skill file.
	localPath := writeSkillFile(t, dir, "local-skill.md", skillMD("local-skill", "Local skill"))

	svc := NewSkillService(cfgPath, storeDir, "")
	if err := svc.Add(context.Background(), localPath, false); err != nil {
		t.Fatalf("Add local path returned error: %v", err)
	}

	destPath := filepath.Join(storeDir, "local-skill.md")
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("expected skill file at %s: %v", destPath, err)
	}
}

func TestSkillServiceAdd_MissingLocalPath(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	svc := NewSkillService(cfgPath, storeDir, "")
	err := svc.Add(context.Background(), "/nonexistent/path/skill.md", false)
	if err == nil {
		t.Fatal("expected error for missing local path")
	}
}

func TestSkillServiceAdd_ShortNameWithRegistry(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	content := skillMD("registry-skill", "From registry")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/registry-skill.md" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(content))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, srv.URL)
	if err := svc.Add(context.Background(), "registry-skill", false); err != nil {
		t.Fatalf("Add short name with registry returned error: %v", err)
	}

	destPath := filepath.Join(storeDir, "registry-skill.md")
	if _, err := os.Stat(destPath); err != nil {
		t.Errorf("expected skill file at %s: %v", destPath, err)
	}
}

func TestSkillServiceAdd_ShortNameWithoutRegistry(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	svc := NewSkillService(cfgPath, storeDir, "") // no registry URL
	err := svc.Add(context.Background(), "some-skill", false)
	if !errors.Is(err, ErrNoRegistry) {
		t.Fatalf("expected ErrNoRegistry, got: %v", err)
	}
}

func TestSkillServiceAdd_ParseFailure(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	// Invalid skill content — tool block with missing required fields.
	invalidContent := "---\nname: bad-skill\ndescription: bad\n---\n\n```yaml tool\nname: \"\"\ndescription: \"\"\ncommand: \"\"\n```\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(invalidContent))
	}))
	defer srv.Close()

	svc := NewSkillService(cfgPath, storeDir, "")
	err := svc.Add(context.Background(), srv.URL+"/bad-skill.md", false)
	if err == nil {
		t.Fatal("expected error for skill with parse failure")
	}

	// The skill file must NOT have been written.
	destPath := filepath.Join(storeDir, "bad-skill.md")
	if _, statErr := os.Stat(destPath); statErr == nil {
		t.Error("skill file should not exist after parse failure")
	}
}

// ---------------------------------------------------------------------------
// SkillService.Remove tests
// ---------------------------------------------------------------------------

func TestSkillServiceRemove_ManagedSkillDeleteFile(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	skillPath := writeSkillFile(t, storeDir, "managed.md", skillMD("managed", "Managed skill"))
	cfgPath := writeTempConfig(t, dir, []string{skillPath})

	svc := NewSkillService(cfgPath, storeDir, "")
	if err := svc.Remove(context.Background(), "managed", true); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	// File should be gone.
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("expected skill file to be deleted")
	}

	// Config should have no skills.
	skills, _ := svc.List(false)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills after remove, got %d", len(skills))
	}
}

func TestSkillServiceRemove_DeleteFileFalse(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	skillPath := writeSkillFile(t, storeDir, "keep-file.md", skillMD("keep-file", "Keep file"))
	cfgPath := writeTempConfig(t, dir, []string{skillPath})

	svc := NewSkillService(cfgPath, storeDir, "")
	if err := svc.Remove(context.Background(), "keep-file", false); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	// File should still exist.
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected skill file to remain, got error: %v", err)
	}

	// Config should have no skills.
	skills, _ := svc.List(false)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills after remove, got %d", len(skills))
	}
}

func TestSkillServiceRemove_ExternalPath_NotDeleted(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	externalDir := filepath.Join(dir, "external")
	if err := os.MkdirAll(externalDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// External skill — NOT under storeDir.
	skillPath := writeSkillFile(t, externalDir, "external.md", skillMD("external", "External skill"))
	cfgPath := writeTempConfig(t, dir, []string{skillPath})

	svc := NewSkillService(cfgPath, storeDir, "")
	if err := svc.Remove(context.Background(), "external", true); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	// File should still exist (not under storeDir).
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected external skill file to remain, got error: %v", err)
	}
}

func TestSkillServiceRemove_NotFound(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	svc := NewSkillService(cfgPath, storeDir, "")
	err := svc.Remove(context.Background(), "nonexistent", false)
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SkillService.Info tests
// ---------------------------------------------------------------------------

func TestSkillServiceInfo_HappyPath(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Skill with both frontmatter and a tool block.
	content := "---\nname: info-skill\ndescription: \"Info skill\"\n---\n\nProse here.\n\n```yaml tool\nname: my_tool\ndescription: does something\ncommand: echo hello\n```\n"
	skillPath := writeSkillFile(t, storeDir, "info-skill.md", content)
	cfgPath := writeTempConfig(t, dir, []string{skillPath})

	svc := NewSkillService(cfgPath, storeDir, "")
	sc, tools, err := svc.Info("info-skill")
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}

	if sc.Name != "info-skill" {
		t.Errorf("expected name 'info-skill', got %q", sc.Name)
	}
	if sc.Description != "Info skill" {
		t.Errorf("expected description 'Info skill', got %q", sc.Description)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "my_tool" {
		t.Errorf("expected tool name 'my_tool', got %q", tools[0].Name)
	}
}

func TestSkillServiceInfo_NotFound(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "store")
	cfgPath := writeTempConfig(t, dir, nil)

	svc := NewSkillService(cfgPath, storeDir, "")
	_, _, err := svc.Info("nonexistent")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got: %v", err)
	}
}
