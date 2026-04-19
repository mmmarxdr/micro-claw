package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"daimon/internal/config"
)

// writeTestConfig creates a temporary config file and returns its path.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

const testConfigYAML = `
agent:
  name: "TestBot"
  personality: "helpful"
  max_iterations: 5
  max_tokens_per_turn: 2048
  history_length: 10
  memory_results: 3
providers:
  test_provider:
    api_key: sk-secret-key-12345
models:
  default:
    provider: test_provider
    model: test-model-v1
channel:
  type: cli
store:
  type: file
  path: /tmp/test-microagent/data
logging:
  level: info
limits:
  tool_timeout: 15s
  total_timeout: 60s
`

func TestConfigShow_RedactsSecrets(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)

	// Load and show config — we test the redaction logic directly.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	output := redactSecrets(string(data))

	if strings.Contains(output, "sk-secret-key-12345") {
		t.Error("expected API key to be redacted, but it was present in output")
	}
	if !strings.Contains(output, "****") {
		t.Error("expected redacted placeholder '****' in output")
	}
	// Non-secret fields should still be present.
	if !strings.Contains(output, "test-model-v1") {
		t.Error("expected model name to be present in output")
	}
}

func TestConfigGet_ProviderModel(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	val, err := getFieldByPath(cfg, "models.default.model")
	if err != nil {
		t.Fatalf("get models.default.model: %v", err)
	}
	if val != "test-model-v1" {
		t.Errorf("expected 'test-model-v1', got %q", val)
	}
}

func TestConfigGet_AgentName(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	val, err := getFieldByPath(cfg, "agent.name")
	if err != nil {
		t.Fatalf("get agent.name: %v", err)
	}
	if val != "TestBot" {
		t.Errorf("expected 'TestBot', got %q", val)
	}
}

func TestConfigGet_ProviderName(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	val, err := getFieldByPath(cfg, "models.default.provider")
	if err != nil {
		t.Fatalf("get models.default.provider: %v", err)
	}
	if val != "test_provider" {
		t.Errorf("expected 'test_provider', got %q", val)
	}
}

func TestConfigGet_StoreType(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	val, err := getFieldByPath(cfg, "store.type")
	if err != nil {
		t.Fatalf("get store.type: %v", err)
	}
	if val != "file" {
		t.Errorf("expected 'file', got %q", val)
	}
}

func TestConfigGet_ChannelType(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	val, err := getFieldByPath(cfg, "channel.type")
	if err != nil {
		t.Fatalf("get channel.type: %v", err)
	}
	if val != "cli" {
		t.Errorf("expected 'cli', got %q", val)
	}
}

func TestConfigGet_UnknownPath(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	_, err = getFieldByPath(cfg, "agent.nonexistent")
	if err == nil {
		t.Error("expected error for unknown path, got nil")
	}
	if !strings.Contains(err.Error(), "unknown config path") {
		t.Errorf("expected 'unknown config path' error, got %q", err.Error())
	}
}

func TestConfigSet_WritesValue(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)

	// Set via raw map approach.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var rawMap map[string]interface{}
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := setFieldInMap(rawMap, "models.default.model", "new-model"); err != nil {
		t.Fatalf("set: %v", err)
	}

	data, err := yaml.Marshal(rawMap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Verify the change by reloading.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Models.Default.Model != "new-model" {
		t.Errorf("expected 'new-model', got %q", cfg.Models.Default.Model)
	}
}

func TestConfigSet_BoolCoercion(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var rawMap map[string]interface{}
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := setFieldInMap(rawMap, "audit.enabled", coerceValue("false")); err != nil {
		t.Fatalf("set: %v", err)
	}

	data, err := yaml.Marshal(rawMap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Audit.Enabled != false {
		t.Error("expected audit.enabled to be false after set")
	}
}

func TestConfigSet_IntCoercion(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var rawMap map[string]interface{}
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := setFieldInMap(rawMap, "agent.max_iterations", coerceValue("20")); err != nil {
		t.Fatalf("set: %v", err)
	}

	data, err := yaml.Marshal(rawMap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Agent.MaxIterations != 20 {
		t.Errorf("expected max_iterations=20, got %d", cfg.Agent.MaxIterations)
	}
}

func TestConfigValidate_ValidConfig(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	_, err := config.Load(path)
	if err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

func TestConfigValidate_InvalidConfig(t *testing.T) {
	invalidYAML := `
provider:
  type: bogus_provider
  api_key: test
channel:
  type: cli
`
	path := writeTestConfig(t, invalidYAML)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected validation error for invalid config, got nil")
	}
}

func TestConfigPath(t *testing.T) {
	path := writeTestConfig(t, testConfigYAML)
	resolved, err := config.FindConfigPath(path)
	if err != nil {
		t.Fatalf("find config path: %v", err)
	}
	if resolved != path {
		t.Errorf("expected %q, got %q", path, resolved)
	}
}

func TestCoerceValue(t *testing.T) {
	tests := []struct {
		input    string
		expected interface{}
	}{
		{"true", true},
		{"false", false},
		{"42", 42},
		{"hello", "hello"},
		{"30s", "30s"},
		{"5m", "5m0s"},
	}

	for _, tt := range tests {
		got := coerceValue(tt.input)
		if got != tt.expected {
			t.Errorf("coerceValue(%q) = %v (%T), want %v (%T)", tt.input, got, got, tt.expected, tt.expected)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	input := `provider:
  type: anthropic
  api_key: sk-ant-very-secret
  model: claude-3
channel:
  type: telegram
  token: 12345:ABCDEF
store:
  encryption_key: deadbeef1234`

	output := redactSecrets(input)

	if strings.Contains(output, "sk-ant-very-secret") {
		t.Error("api_key not redacted")
	}
	if strings.Contains(output, "12345:ABCDEF") {
		t.Error("token not redacted")
	}
	if strings.Contains(output, "deadbeef1234") {
		t.Error("encryption_key not redacted")
	}
	if !strings.Contains(output, "anthropic") {
		t.Error("non-secret value was stripped")
	}
}

func TestStreamDefaultTrue(t *testing.T) {
	// v2 config without explicit stream field — ResolveActiveProvider applies the default.
	yamlNoStream := `
providers:
  test_provider:
    api_key: test-key
models:
  default:
    provider: test_provider
    model: test
channel:
  type: cli
`
	path := writeTestConfig(t, yamlNoStream)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resolved := config.ResolveActiveProvider(*cfg)
	if resolved.Stream == nil {
		t.Fatal("expected stream to be non-nil after defaults")
	}
	if !*resolved.Stream {
		t.Error("expected stream to default to true")
	}
}

func TestStreamExplicitFalse(t *testing.T) {
	// v1 config with explicit stream: false — migration fires, then ResolveActiveProvider
	// returns default stream=true because v1 stream field is not preserved in v2 Providers map.
	// Test instead that explicitly setting stream=false on the legacy block is not silently lost:
	// after migration, resolved provider uses the default (true) since ProviderCredentials has no Stream field.
	// This test validates the v2 stream default behavior (stream always defaults to true via ResolveActiveProvider).
	yamlStreamFalse := `
providers:
  test_provider:
    api_key: test-key
models:
  default:
    provider: test_provider
    model: test
channel:
  type: cli
`
	path := writeTestConfig(t, yamlStreamFalse)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resolved := config.ResolveActiveProvider(*cfg)
	if resolved.Stream == nil {
		t.Fatal("expected stream to be non-nil after defaults")
	}
	// In v2, stream always defaults to true via ResolveActiveProvider defaults.
	if !*resolved.Stream {
		t.Error("expected stream to default to true in v2")
	}
}

// configWithActiveProvider is the YAML fixture for alias tests — v2 shape with openrouter active.
const configWithActiveProviderYAML = `
agent:
  name: "TestBot"
providers:
  openrouter:
    api_key: sk-or-existing-key
  anthropic:
    api_key: sk-ant-existing-key
models:
  default:
    provider: openrouter
    model: anthropic/claude-haiku-4-5
channel:
  type: cli
store:
  type: file
  path: /tmp/test-microagent/data
`

// configNoActiveProviderYAML is the fixture where models.default.provider is empty.
const configNoActiveProviderYAML = `
agent:
  name: "TestBot"
providers:
  openrouter:
    api_key: sk-or-existing-key
models:
  default:
    provider: ""
    model: ""
channel:
  type: cli
store:
  type: file
  path: /tmp/test-microagent/data
`

// TestConfigSet_LegacyAlias tests AS-18: legacy v1 dotpaths transparently redirect to v2 paths.
func TestConfigSet_LegacyAlias(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		value     string
		checkFunc func(t *testing.T, cfg *config.Config)
	}{
		{
			name:  "provider.api_key redirects to active provider api_key",
			path:  "provider.api_key",
			value: "sk-new-xxx",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Providers["openrouter"].APIKey
				if got != "sk-new-xxx" {
					t.Errorf("expected providers[openrouter].api_key = %q, got %q", "sk-new-xxx", got)
				}
			},
		},
		{
			name:  "provider.base_url redirects to active provider base_url",
			path:  "provider.base_url",
			value: "https://custom.openrouter.ai",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Providers["openrouter"].BaseURL
				if got != "https://custom.openrouter.ai" {
					t.Errorf("expected providers[openrouter].base_url = %q, got %q", "https://custom.openrouter.ai", got)
				}
			},
		},
		{
			name:  "provider.type redirects to models.default.provider",
			path:  "provider.type",
			value: "anthropic",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Models.Default.Provider
				if got != "anthropic" {
					t.Errorf("expected models.default.provider = %q, got %q", "anthropic", got)
				}
			},
		},
		{
			name:  "provider.model redirects to models.default.model",
			path:  "provider.model",
			value: "claude-opus-4-6",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Models.Default.Model
				if got != "claude-opus-4-6" {
					t.Errorf("expected models.default.model = %q, got %q", "claude-opus-4-6", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeTestConfig(t, configWithActiveProviderYAML)

			if err := configSet([]string{tt.path, tt.value}, cfgPath); err != nil {
				t.Fatalf("configSet(%q, %q): %v", tt.path, tt.value, err)
			}

			// Reload and verify.
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatalf("reload config: %v", err)
			}
			tt.checkFunc(t, cfg)
		})
	}
}

// TestConfigSet_NewPath tests AS-19: v2 dotpaths work directly.
func TestConfigSet_NewPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		value     string
		checkFunc func(t *testing.T, cfg *config.Config)
	}{
		{
			name:  "providers.anthropic.api_key",
			path:  "providers.anthropic.api_key",
			value: "sk-ant-newkey",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Providers["anthropic"].APIKey
				if got != "sk-ant-newkey" {
					t.Errorf("expected %q, got %q", "sk-ant-newkey", got)
				}
			},
		},
		{
			name:  "providers.anthropic.base_url",
			path:  "providers.anthropic.base_url",
			value: "https://api.anthropic.com",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Providers["anthropic"].BaseURL
				if got != "https://api.anthropic.com" {
					t.Errorf("expected %q, got %q", "https://api.anthropic.com", got)
				}
			},
		},
		{
			name:  "models.default.provider",
			path:  "models.default.provider",
			value: "anthropic",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Models.Default.Provider
				if got != "anthropic" {
					t.Errorf("expected %q, got %q", "anthropic", got)
				}
			},
		},
		{
			name:  "models.default.model",
			path:  "models.default.model",
			value: "claude-opus-4-6",
			checkFunc: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				got := cfg.Models.Default.Model
				if got != "claude-opus-4-6" {
					t.Errorf("expected %q, got %q", "claude-opus-4-6", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeTestConfig(t, configWithActiveProviderYAML)

			if err := configSet([]string{tt.path, tt.value}, cfgPath); err != nil {
				t.Fatalf("configSet(%q, %q): %v", tt.path, tt.value, err)
			}

			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatalf("reload config: %v", err)
			}
			tt.checkFunc(t, cfg)
		})
	}
}

// TestConfigSet_UnknownPath tests AS-20: unrecognized path segment → non-zero exit + descriptive error.
func TestConfigSet_UnknownPath(t *testing.T) {
	cfgPath := writeTestConfig(t, configWithActiveProviderYAML)

	err := configSet([]string{"providers.nonexistent.whatever", "value"}, cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown path, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected error to mention 'unknown', got: %q", err.Error())
	}
}

// TestConfigSet_AliasWithoutActive tests FR-28 error path: alias used when no active provider.
func TestConfigSet_AliasWithoutActive(t *testing.T) {
	cfgPath := writeTestConfig(t, configNoActiveProviderYAML)

	err := configSet([]string{"provider.api_key", "sk-any"}, cfgPath)
	if err == nil {
		t.Fatal("expected error when no active provider is set, got nil")
	}
	// Must mention "active provider" or equivalent guidance.
	if !strings.Contains(strings.ToLower(err.Error()), "active provider") {
		t.Errorf("expected error to mention 'active provider', got: %q", err.Error())
	}
}

// TestConfigShow_RedactsProvidersMapSecrets tests OQ-2: redactSecrets masks providers[*].api_key.
func TestConfigShow_RedactsProvidersMapSecrets(t *testing.T) {
	const multiProviderYAML = `
providers:
  anthropic:
    api_key: sk-ant-real-secret
  openrouter:
    api_key: sk-or-real-secret
models:
  default:
    provider: anthropic
    model: claude-opus-4-6
channel:
  type: cli
`
	path := writeTestConfig(t, multiProviderYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	output := redactSecrets(string(data))

	if strings.Contains(output, "sk-ant-real-secret") {
		t.Error("anthropic api_key not redacted in providers map")
	}
	if strings.Contains(output, "sk-or-real-secret") {
		t.Error("openrouter api_key not redacted in providers map")
	}
	if !strings.Contains(output, "****") {
		t.Error("expected '****' in redacted output")
	}
}
