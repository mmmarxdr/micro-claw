package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"daimon/internal/config"
)

func newOllamaTestProvider(t *testing.T) *OllamaProvider {
	t.Helper()
	cfg := config.ProviderConfig{
		Type:    "ollama",
		Model:   "llama3.2",
		BaseURL: "http://localhost:11434/v1",
		// api_key intentionally empty — Ollama does not require one
	}
	p, err := NewOllamaProvider(cfg)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	return p
}

func TestOllamaProvider_Capabilities(t *testing.T) {
	p := newOllamaTestProvider(t)

	if got := p.SupportsMultimodal(); got != false {
		t.Errorf("SupportsMultimodal() = %v, want false", got)
	}
	if got := p.SupportsAudio(); got != false {
		t.Errorf("SupportsAudio() = %v, want false", got)
	}
	if got := p.Name(); got != "ollama" {
		t.Errorf("Name() = %q, want %q", got, "ollama")
	}
}

func TestOllamaProvider_SupportsToolsDelegates(t *testing.T) {
	p := newOllamaTestProvider(t)
	// SupportsTools() must delegate to the embedded OpenAIProvider (returns true).
	if got := p.SupportsTools(); got != true {
		t.Errorf("SupportsTools() = %v, want true (delegated from OpenAIProvider)", got)
	}
}

// --------------------------------------------------------------------------
// Phase 4.1 — OllamaProvider.ListModels()
// --------------------------------------------------------------------------

func newOllamaProviderWithBaseURL(t *testing.T, baseURL string) *OllamaProvider {
	t.Helper()
	cfg := config.ProviderConfig{
		Type:    "ollama",
		Model:   "llama3:latest",
		BaseURL: baseURL + "/v1", // OpenAI-compat path for Chat
	}
	p, err := NewOllamaProvider(cfg)
	if err != nil {
		t.Fatalf("NewOllamaProvider: %v", err)
	}
	return p
}

func TestOllamaProvider_ListModels_Success(t *testing.T) {
	// /api/tags returns two models; assert mapping to ModelInfo
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path %q, want /api/tags", r.URL.Path)
		}
		resp := map[string]any{
			"models": []any{
				map[string]any{"name": "llama3:latest"},
				map[string]any{"name": "mistral:7b"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	p := newOllamaProviderWithBaseURL(t, ts.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %v", len(models), models)
	}

	wantModels := []ModelInfo{
		{ID: "llama3:latest", Name: "llama3:latest", Free: true},
		{ID: "mistral:7b", Name: "mistral:7b", Free: true},
	}
	for i, want := range wantModels {
		got := models[i]
		if got.ID != want.ID {
			t.Errorf("models[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Name != want.Name {
			t.Errorf("models[%d].Name = %q, want %q", i, got.Name, want.Name)
		}
		if got.Free != want.Free {
			t.Errorf("models[%d].Free = %v, want %v", i, got.Free, want.Free)
		}
	}
}

func TestOllamaProvider_ListModels_ServerError(t *testing.T) {
	// Non-200 response returns a non-nil error, no panic
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	p := newOllamaProviderWithBaseURL(t, ts.URL)
	models, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatalf("expected error from non-200 response, got models: %v", models)
	}
	if models != nil {
		t.Errorf("expected nil models on error, got %v", models)
	}
}

func TestOllamaProvider_ListModels_ConnectionRefused(t *testing.T) {
	// Unreachable server returns error, no panic
	p := newOllamaProviderWithBaseURL(t, fmt.Sprintf("http://127.0.0.1:%d", 19999))
	models, err := p.ListModels(context.Background())
	if err == nil {
		t.Fatalf("expected error from connection refused, got models: %v", models)
	}
	if models != nil {
		t.Errorf("expected nil models on error, got %v", models)
	}
}

func TestOllamaProvider_ListModels_EmptyResponse(t *testing.T) {
	// Empty models array returns empty slice, no error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ts.Close()

	p := newOllamaProviderWithBaseURL(t, ts.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 models for empty response, got %d", len(models))
	}
}
