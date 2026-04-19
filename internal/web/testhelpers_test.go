package web

import (
	"context"

	"daimon/internal/config"
	"daimon/internal/provider"
)

// minimalConfig returns a *config.Config suitable for unit tests (v2 shape).
// Provider is "anthropic" with model "claude-test" for backward compatibility
// with integration tests that check the status endpoint.
func minimalConfig() *config.Config {
	return &config.Config{
		Agent: config.AgentConfig{Name: "test-agent"},
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-test"},
		},
		Models:  config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-test"}},
		Channel: config.ChannelConfig{Type: "cli"},
		Web:     config.WebConfig{Host: "127.0.0.1", Port: 8080},
	}
}

// newSetupTestServer creates a minimal Server for setup handler tests.
func newSetupTestServer(cfg *config.Config) *Server {
	return NewServer(ServerDeps{Config: cfg})
}

// newSetupTestServerWithFactory creates a Server with a custom provider factory for T05 tests.
func newSetupTestServerWithFactory(cfg *config.Config, factory providerFactory) *Server {
	deps := ServerDeps{
		Config:          cfg,
		ProviderFactory: factory,
	}
	return NewServer(deps)
}

// newSetupTestServerWithConfigPath creates a Server with a writable config path for T06/T07 tests.
func newSetupTestServerWithConfigPath(cfg *config.Config, cfgPath string) *Server {
	deps := ServerDeps{
		Config:     cfg,
		ConfigPath: cfgPath,
	}
	return NewServer(deps)
}

// mockProviderFactory returns a providerFactory that creates a mock provider.
// If chatErr is non-nil, the mock's HealthCheck returns that error.
func mockProviderFactory(chatErr error) providerFactory {
	return func(cfg config.ProviderConfig) (provider.Provider, error) {
		return &mockProvider{healthErr: chatErr}, nil
	}
}

// mockProvider is a minimal Provider implementation for tests.
type mockProvider struct {
	healthErr error
}

func (m *mockProvider) Name() string  { return "mock" }
func (m *mockProvider) Model() string { return "mock-model" }
func (m *mockProvider) Chat(_ context.Context, _ provider.ChatRequest) (*provider.ChatResponse, error) {
	if m.healthErr != nil {
		return nil, m.healthErr
	}
	return &provider.ChatResponse{Content: "ok"}, nil
}
func (m *mockProvider) SupportsTools() bool      { return false }
func (m *mockProvider) SupportsMultimodal() bool { return false }
func (m *mockProvider) SupportsAudio() bool      { return false }
func (m *mockProvider) HealthCheck(_ context.Context) (string, error) {
	if m.healthErr != nil {
		return "", m.healthErr
	}
	return "ok", nil
}

// Ensure mockProvider satisfies the interface at compile time.
var _ provider.Provider = (*mockProvider)(nil)
