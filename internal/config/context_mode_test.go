package config

import (
	"os"
	"testing"
	"time"
)

func TestContextModeConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    ContextMode
		expected string
	}{
		{"off", ContextModeOff, "off"},
		{"conservative", ContextModeConservative, "conservative"},
		{"auto", ContextModeAuto, "auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.value) != tt.expected {
				t.Errorf("ContextMode %s = %q, want %q", tt.name, tt.value, tt.expected)
			}
		})
	}
}

func TestContextModeConfig_Defaults(t *testing.T) {
	// Create a Config with no ContextMode specified
	yamlData := `
agent:
  name: "TestAgent"
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Test that defaults are applied
	ctxMode := cfg.Agent.ContextMode

	// Default mode should be "off"
	if ctxMode.Mode != ContextModeOff {
		t.Errorf("Default Mode = %q, want %q", ctxMode.Mode, ContextModeOff)
	}

	// Default ShellMaxOutput should be 4096 for auto, but we're in off mode
	// In off mode, ShellMaxOutput should be 0 or not applied?
	// From design: ShellMaxOutput defaults: 4096 (auto), 8192 (conservative)
	// Off mode doesn't use these values, but they should still be set for consistency
	// Actually, looking at design defaults: these only apply when mode is auto/conservative
	// Let's test that when Mode is off, these defaults might be different
	// For now, just ensure struct is populated
}

func TestContextModeConfig_AutoModeDefaults(t *testing.T) {
	yamlData := `
agent:
  name: "TestAgent"
  context_mode:
    mode: "auto"
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	ctxMode := cfg.Agent.ContextMode

	// In auto mode, ShellMaxOutput should default to 4096
	if ctxMode.ShellMaxOutput != 4096 {
		t.Errorf("Auto mode ShellMaxOutput = %d, want 4096", ctxMode.ShellMaxOutput)
	}

	// FileChunkSize should default to 2000 in auto mode
	if ctxMode.FileChunkSize != 2000 {
		t.Errorf("Auto mode FileChunkSize = %d, want 2000", ctxMode.FileChunkSize)
	}

	// SandboxTimeout should default to 30s
	if ctxMode.SandboxTimeout != 30*time.Second {
		t.Errorf("Auto mode SandboxTimeout = %v, want 30s", ctxMode.SandboxTimeout)
	}

	// AutoIndexOutputs should default to true in auto mode
	if ctxMode.AutoIndexOutputs == nil || !*ctxMode.AutoIndexOutputs {
		t.Errorf("Auto mode AutoIndexOutputs = %v, want true (pointer: %v)",
			ctxMode.AutoIndexOutputs, BoolVal(ctxMode.AutoIndexOutputs))
	}

	// SandboxKeepFirst should default to 20
	if ctxMode.SandboxKeepFirst != 20 {
		t.Errorf("Auto mode SandboxKeepFirst = %d, want 20", ctxMode.SandboxKeepFirst)
	}

	// SandboxKeepLast should default to 10
	if ctxMode.SandboxKeepLast != 10 {
		t.Errorf("Auto mode SandboxKeepLast = %d, want 10", ctxMode.SandboxKeepLast)
	}
}

func TestContextModeConfig_ConservativeModeDefaults(t *testing.T) {
	yamlData := `
agent:
  name: "TestAgent"
  context_mode:
    mode: "conservative"
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	ctxMode := cfg.Agent.ContextMode

	// In conservative mode, ShellMaxOutput should default to 8192
	if ctxMode.ShellMaxOutput != 8192 {
		t.Errorf("Conservative mode ShellMaxOutput = %d, want 8192", ctxMode.ShellMaxOutput)
	}

	// FileChunkSize should default to 4000 in conservative mode
	if ctxMode.FileChunkSize != 4000 {
		t.Errorf("Conservative mode FileChunkSize = %d, want 4000", ctxMode.FileChunkSize)
	}

	// SandboxTimeout should default to 30s (same for all modes)
	if ctxMode.SandboxTimeout != 30*time.Second {
		t.Errorf("Conservative mode SandboxTimeout = %v, want 30s", ctxMode.SandboxTimeout)
	}

	// AutoIndexOutputs should default to false in conservative mode (nil pointer means false after BoolVal)
	if ctxMode.AutoIndexOutputs != nil && *ctxMode.AutoIndexOutputs {
		t.Errorf("Conservative mode AutoIndexOutputs = %v, want false (pointer: %v)",
			ctxMode.AutoIndexOutputs, BoolVal(ctxMode.AutoIndexOutputs))
	}

	// SandboxKeepFirst/Last same defaults as other modes
	if ctxMode.SandboxKeepFirst != 20 {
		t.Errorf("Conservative mode SandboxKeepFirst = %d, want 20", ctxMode.SandboxKeepFirst)
	}
	if ctxMode.SandboxKeepLast != 10 {
		t.Errorf("Conservative mode SandboxKeepLast = %d, want 10", ctxMode.SandboxKeepLast)
	}
}

func TestContextModeConfig_CustomValues(t *testing.T) {
	yamlData := `
agent:
  name: "TestAgent"
  context_mode:
    mode: "auto"
    shell_max_output: 2048
    file_chunk_size: 1000
    sandbox_timeout: 15s
    auto_index_outputs: false
    sandbox_keep_first: 5
    sandbox_keep_last: 3
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
`

	tmpFile := createTempFile(t, yamlData)
	defer os.Remove(tmpFile)

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	ctxMode := cfg.Agent.ContextMode

	// Custom values should be respected
	if ctxMode.ShellMaxOutput != 2048 {
		t.Errorf("Custom ShellMaxOutput = %d, want 2048", ctxMode.ShellMaxOutput)
	}
	if ctxMode.FileChunkSize != 1000 {
		t.Errorf("Custom FileChunkSize = %d, want 1000", ctxMode.FileChunkSize)
	}
	if ctxMode.SandboxTimeout != 15*time.Second {
		t.Errorf("Custom SandboxTimeout = %v, want 15s", ctxMode.SandboxTimeout)
	}
	if ctxMode.AutoIndexOutputs == nil || *ctxMode.AutoIndexOutputs {
		t.Errorf("Custom AutoIndexOutputs = %v, want false (pointer: %v)",
			ctxMode.AutoIndexOutputs, BoolVal(ctxMode.AutoIndexOutputs))
	}
	if ctxMode.SandboxKeepFirst != 5 {
		t.Errorf("Custom SandboxKeepFirst = %d, want 5", ctxMode.SandboxKeepFirst)
	}
	if ctxMode.SandboxKeepLast != 3 {
		t.Errorf("Custom SandboxKeepLast = %d, want 3", ctxMode.SandboxKeepLast)
	}
}
