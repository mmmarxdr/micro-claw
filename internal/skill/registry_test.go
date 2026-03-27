package skill

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sampleRegistryYAML is a minimal valid registry YAML for tests.
const sampleRegistryYAML = `
skills:
  - name: git-helper
    description: Git workflow assistant
    url: https://example.com/skills/git-helper.md
    version: "1.0.0"
    tags: [git, development]
  - name: cron
    description: Natural language cron scheduling
    url: https://example.com/skills/cron.md
    version: "1.0.0"
    tags: [scheduling, automation]
`

// ---------------------------------------------------------------------------
// FetchRegistry tests
// ---------------------------------------------------------------------------

func TestFetchRegistry_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sampleRegistryYAML))
	}))
	defer srv.Close()

	reg, err := FetchRegistry(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchRegistry returned error: %v", err)
	}
	if len(reg.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(reg.Skills))
	}
	if reg.Skills[0].Name != "git-helper" {
		t.Errorf("expected first skill 'git-helper', got %q", reg.Skills[0].Name)
	}
	if reg.Skills[1].Name != "cron" {
		t.Errorf("expected second skill 'cron', got %q", reg.Skills[1].Name)
	}
}

func TestFetchRegistry_Non200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := FetchRegistry(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for non-200 HTTP response")
	}
}

func TestFetchRegistry_InvalidYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// "key: [unclosed" is a structurally invalid YAML that gopkg.in/yaml.v3 rejects.
		w.Write([]byte("key: [unclosed"))
	}))
	defer srv.Close()

	_, err := FetchRegistry(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for invalid YAML response")
	}
}

func TestFetchRegistry_EmptyRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("skills: []\n"))
	}))
	defer srv.Close()

	reg, err := FetchRegistry(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchRegistry returned error: %v", err)
	}
	if len(reg.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(reg.Skills))
	}
}

// ---------------------------------------------------------------------------
// Registry.Resolve tests
// ---------------------------------------------------------------------------

func registryFromYAML(t *testing.T) *Registry {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sampleRegistryYAML))
	}))
	defer srv.Close()
	reg, err := FetchRegistry(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchRegistry: %v", err)
	}
	return reg
}

func TestRegistryResolve_ExactMatch(t *testing.T) {
	reg := registryFromYAML(t)

	entry, ok := reg.Resolve("git-helper")
	if !ok {
		t.Fatal("expected to resolve 'git-helper'")
	}
	if entry.Name != "git-helper" {
		t.Errorf("expected name 'git-helper', got %q", entry.Name)
	}
	if entry.URL == "" {
		t.Error("expected non-empty URL")
	}
}

func TestRegistryResolve_CaseInsensitive(t *testing.T) {
	reg := registryFromYAML(t)

	entry, ok := reg.Resolve("GIT-HELPER")
	if !ok {
		t.Fatal("expected case-insensitive match for 'GIT-HELPER'")
	}
	if entry.Name != "git-helper" {
		t.Errorf("expected name 'git-helper', got %q", entry.Name)
	}
}

func TestRegistryResolve_NotFound(t *testing.T) {
	reg := registryFromYAML(t)

	_, ok := reg.Resolve("nonexistent-skill")
	if ok {
		t.Fatal("expected resolve to return false for unknown skill")
	}
}

func TestRegistryResolve_SecondEntry(t *testing.T) {
	reg := registryFromYAML(t)

	entry, ok := reg.Resolve("cron")
	if !ok {
		t.Fatal("expected to resolve 'cron'")
	}
	if entry.Name != "cron" {
		t.Errorf("expected name 'cron', got %q", entry.Name)
	}
}

// ---------------------------------------------------------------------------
// Registry.Search tests
// ---------------------------------------------------------------------------

func TestRegistrySearch_EmptyQuery_ReturnsAll(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for empty query, got %d", len(results))
	}
}

func TestRegistrySearch_ByName(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("git")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for query 'git', got %d", len(results))
	}
	if results[0].Name != "git-helper" {
		t.Errorf("expected 'git-helper', got %q", results[0].Name)
	}
}

func TestRegistrySearch_ByDescription(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("scheduling")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for query 'scheduling', got %d", len(results))
	}
	if results[0].Name != "cron" {
		t.Errorf("expected 'cron', got %q", results[0].Name)
	}
}

func TestRegistrySearch_ByTag(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("automation")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for tag query 'automation', got %d", len(results))
	}
	if results[0].Name != "cron" {
		t.Errorf("expected 'cron', got %q", results[0].Name)
	}
}

func TestRegistrySearch_NoMatch(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("kubernetes-operator")
	if len(results) != 0 {
		t.Errorf("expected 0 results for unmatched query, got %d", len(results))
	}
}

func TestRegistrySearch_CaseInsensitive(t *testing.T) {
	reg := registryFromYAML(t)

	results := reg.Search("GIT")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for case-insensitive query 'GIT', got %d", len(results))
	}
}
