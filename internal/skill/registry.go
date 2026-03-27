package skill

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RegistryEntry describes a skill available in the remote registry.
type RegistryEntry struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	URL         string   `yaml:"url"`
	Version     string   `yaml:"version"`
	Tags        []string `yaml:"tags"`
}

// Registry holds the parsed contents of a remote skills registry index.
type Registry struct {
	Skills []RegistryEntry `yaml:"skills"`
}

// FetchRegistry fetches and parses the registry YAML from the given URL.
// A 15-second HTTP timeout is applied. The response is limited to 1 MB.
func FetchRegistry(ctx context.Context, registryURL string) (*Registry, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create registry request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch registry %s: %w", registryURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch registry %s: HTTP %d", registryURL, resp.StatusCode)
	}

	const maxSize = 1 << 20 // 1 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read registry response: %w", err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("registry response exceeds 1 MB limit")
	}

	var reg Registry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse registry YAML: %w", err)
	}

	return &reg, nil
}

// Resolve finds the registry entry whose name matches the given name
// (case-insensitive). Returns the entry and true if found, or nil and false.
func (r *Registry) Resolve(name string) (*RegistryEntry, bool) {
	lower := strings.ToLower(name)
	for i := range r.Skills {
		if strings.ToLower(r.Skills[i].Name) == lower {
			return &r.Skills[i], true
		}
	}
	return nil, false
}

// Search returns all registry entries whose name, description, or tags contain
// the query string (case-insensitive). An empty query returns all entries.
func (r *Registry) Search(query string) []RegistryEntry {
	if query == "" {
		result := make([]RegistryEntry, len(r.Skills))
		copy(result, r.Skills)
		return result
	}

	lower := strings.ToLower(query)
	var matches []RegistryEntry
	for _, e := range r.Skills {
		if strings.Contains(strings.ToLower(e.Name), lower) ||
			strings.Contains(strings.ToLower(e.Description), lower) ||
			tagsContain(e.Tags, lower) {
			matches = append(matches, e)
		}
	}
	return matches
}

// tagsContain reports whether any tag contains the substring s (already lowercased).
func tagsContain(tags []string, s string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), s) {
			return true
		}
	}
	return false
}

// availableNames returns a comma-separated list of skill names in the registry.
func (r *Registry) availableNames() string {
	names := make([]string, len(r.Skills))
	for i, e := range r.Skills {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}
