package provider_test

import (
	"context"
	"testing"

	"daimon/internal/config"
	"daimon/internal/provider"
)

// 6.1.1 — NewStaticRegistry with Anthropic key yields a valid Lister; unknown returns false.

func TestRegistry_NewStaticRegistry_KnownProvider_HasLister(t *testing.T) {
	cfg := config.Config{
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-test"},
		},
		Models: config.ModelsConfig{
			Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"},
		},
	}

	reg := provider.NewStaticRegistry(cfg)

	lister, ok := reg.Lister("anthropic")
	if !ok {
		t.Fatal("expected anthropic lister to be found")
	}
	if lister == nil {
		t.Fatal("expected non-nil lister")
	}
}

func TestRegistry_NewStaticRegistry_UnknownProvider_ReturnsFalse(t *testing.T) {
	cfg := config.Config{
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-test"},
		},
		Models: config.ModelsConfig{
			Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"},
		},
	}

	reg := provider.NewStaticRegistry(cfg)

	lister, ok := reg.Lister("badprovider")
	if ok {
		t.Fatal("expected false for unknown provider")
	}
	if lister != nil {
		t.Fatal("expected nil lister for unknown provider")
	}
}

// 6.1.2 — RegisterTransient makes a provider available via Lister.

func TestRegistry_RegisterTransient_MakesListerAvailable(t *testing.T) {
	reg := provider.NewStaticRegistry(config.Config{}) // empty config

	// Before transient registration.
	_, ok := reg.Lister("anthropic")
	if ok {
		t.Fatal("expected no anthropic lister before RegisterTransient")
	}

	// Register a transient lister.
	fake := &fakeModelListerProvider{}
	reg.RegisterTransient("anthropic", fake)

	lister, ok := reg.Lister("anthropic")
	if !ok {
		t.Fatal("expected anthropic lister after RegisterTransient")
	}
	if lister != fake {
		t.Fatal("expected the transient lister to be returned")
	}
}

// 6.1.3 — Providers without API keys are NOT registered (except ollama).

func TestRegistry_NewStaticRegistry_NoAPIKey_NotRegistered(t *testing.T) {
	cfg := config.Config{
		Providers: map[string]config.ProviderCredentials{
			"openai": {APIKey: ""},      // no key — should be skipped
			"ollama": {BaseURL: "http://localhost:11434"}, // ollama exemption
		},
	}

	reg := provider.NewStaticRegistry(cfg)

	_, ok := reg.Lister("openai")
	if ok {
		t.Error("expected openai to NOT be registered when api_key is empty")
	}

	_, ok = reg.Lister("ollama")
	if !ok {
		t.Error("expected ollama to be registered even without api_key")
	}
}

// 6.1.4 — Multiple providers with keys are all registered.

func TestRegistry_NewStaticRegistry_MultipleProviders(t *testing.T) {
	cfg := config.Config{
		Providers: map[string]config.ProviderCredentials{
			"anthropic":  {APIKey: "sk-ant-test"},
			"openai":     {APIKey: "sk-openai-test"},
			"openrouter": {APIKey: "sk-or-test"},
		},
	}

	reg := provider.NewStaticRegistry(cfg)

	for _, name := range []string{"anthropic", "openai", "openrouter"} {
		lister, ok := reg.Lister(name)
		if !ok {
			t.Errorf("expected %s lister, got false", name)
		}
		if lister == nil {
			t.Errorf("expected non-nil lister for %s", name)
		}
	}
}

// fakeModelListerProvider satisfies both Provider and ModelLister for test injection.
type fakeModelListerProvider struct{}

func (f *fakeModelListerProvider) Name() string                          { return "fake" }
func (f *fakeModelListerProvider) Model() string                         { return "fake-model" }
func (f *fakeModelListerProvider) Chat(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	return nil, nil
}
func (f *fakeModelListerProvider) SupportsTools() bool       { return false }
func (f *fakeModelListerProvider) SupportsMultimodal() bool  { return false }
func (f *fakeModelListerProvider) SupportsAudio() bool       { return false }
func (f *fakeModelListerProvider) HealthCheck(ctx context.Context) (string, error) {
	return "fake-model", nil
}
func (f *fakeModelListerProvider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "fake-1", Name: "Fake 1"}}, nil
}
