package provider

import "microagent/internal/config"

// Compile-time interface assertion.
var _ Provider = (*OllamaProvider)(nil)

// OllamaProvider is a thin wrapper around OpenAIProvider that overrides the
// capability methods to reflect Ollama's text-only nature.
//
// Ollama is compatible with the OpenAI Chat Completions API but most models
// are text-only. Returning false for SupportsMultimodal and SupportsAudio
// triggers the agent loop's graceful-degradation path (DegradationNotice)
// instead of sending image/audio blocks that would cause opaque API errors.
type OllamaProvider struct {
	*OpenAIProvider
}

// NewOllamaProvider constructs an OllamaProvider from cfg.
// It delegates construction to NewOpenAIProvider (which handles base_url,
// model defaults, timeout, etc.) and wraps the result.
func NewOllamaProvider(cfg config.ProviderConfig) (*OllamaProvider, error) {
	inner, err := NewOpenAIProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &OllamaProvider{OpenAIProvider: inner}, nil
}

// Name returns "ollama" so logs and token-estimation maps use the correct key.
func (o *OllamaProvider) Name() string { return "ollama" }

// SupportsMultimodal returns false — most Ollama models are text-only.
// The agent loop checks this before Chat() and prepends a DegradationNotice
// when false, so image blocks never reach the Ollama API.
func (o *OllamaProvider) SupportsMultimodal() bool { return false }

// SupportsAudio returns false — Ollama does not support audio input.
func (o *OllamaProvider) SupportsAudio() bool { return false }
