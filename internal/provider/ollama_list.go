package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ollamaTagsResponse is the wire format returned by GET /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// ListModels implements ModelLister for OllamaProvider.
// It calls GET {baseURL}/api/tags and maps the response to []ModelInfo.
// The base URL is derived from the configured OpenAI-compat base URL by
// stripping the /v1 suffix if present.
// Defaults to http://localhost:11434 when no base URL is configured.
func (o *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	// Derive the Ollama API root from the embedded OpenAI provider's baseURL.
	// OllamaProvider is configured with BaseURL = "http://host:port/v1" (OpenAI-compat).
	// /api/tags lives at "http://host:port/api/tags".
	apiBase := o.OpenAIProvider.baseURL
	if apiBase == "" {
		apiBase = "http://localhost:11434/v1"
	}
	// Strip /v1 suffix to get the Ollama server root.
	root := strings.TrimSuffix(apiBase, "/v1")
	if root == "" {
		root = "http://localhost:11434"
	}

	url := root + "/api/tags"

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: creating tags request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: fetching models: %w", wrapNetworkError(err))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: /api/tags returned %d: %s", resp.StatusCode, string(body))
	}

	var result ollamaTagsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("ollama: parsing tags response: %w", err)
	}

	models := make([]ModelInfo, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, ModelInfo{
			ID:   m.Name,
			Name: m.Name,
			Free: true,
		})
	}
	return models, nil
}
