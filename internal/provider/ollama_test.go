package provider

import (
	"testing"

	"microagent/internal/config"
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
