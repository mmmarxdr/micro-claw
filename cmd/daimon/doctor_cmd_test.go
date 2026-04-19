package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoctorCommand_StorePath(t *testing.T) {
	tests := []struct {
		name        string
		storePath   string
		shouldExist bool
		shouldBeDir bool
		writable    bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "writable directory",
			storePath:   "test-store",
			shouldExist: true,
			shouldBeDir: true,
			writable:    true,
			wantErr:     false,
		},
		{
			name:        "non-existent directory",
			storePath:   "nonexistent",
			shouldExist: false,
			wantErr:     true,
			errContains: "does not exist",
		},
		{
			name:        "path is a file, not directory",
			storePath:   "store-file",
			shouldExist: true,
			shouldBeDir: false,
			wantErr:     true,
			errContains: "not a directory",
		},
		{
			name:        "non-writable directory",
			storePath:   "readonly-dir",
			shouldExist: true,
			shouldBeDir: true,
			writable:    false,
			wantErr:     true,
			errContains: "not writable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			// Create test directory structure
			fullStorePath := filepath.Join(tmpDir, tt.storePath)

			if tt.shouldExist {
				if tt.shouldBeDir {
					// Create directory
					if err := os.MkdirAll(fullStorePath, 0755); err != nil {
						t.Fatalf("Failed to create test directory: %v", err)
					}

					if !tt.writable {
						// Make directory read-only
						if err := os.Chmod(fullStorePath, 0555); err != nil {
							t.Fatalf("Failed to make directory read-only: %v", err)
						}
						// Restore permissions after test
						defer os.Chmod(fullStorePath, 0755)
					}
				} else {
					// Create a file instead of directory
					if err := os.WriteFile(fullStorePath, []byte("not a directory"), 0644); err != nil {
						t.Fatalf("Failed to create test file: %v", err)
					}
				}
			}

			// Write config with store path
			configContent := `
agent:
  name: "test"
provider:
  type: "ollama"
  model: "llama3.2"
channel:
  type: "cli"
store:
  type: "file"
  path: "` + fullStorePath + `"
`
			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				t.Fatalf("Failed to write test config: %v", err)
			}

			err := runDoctorCommand([]string{}, configPath)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestRunDoctorCommand_EnvVars(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		setEnv        map[string]string
		wantErr       bool
		errContains   string
	}{
		{
			name: "missing env var",
			configContent: `
agent:
  name: "test"
provider:
  type: "openai"
  model: "gpt-4"
  api_key: "${OPENAI_API_KEY}"  # Will be missing
channel:
  type: "cli"
store:
  type: "file"
  path: "/tmp"
`,
			setEnv: map[string]string{
				// OPENAI_API_KEY not set
			},
			wantErr:     true,
			errContains: "OPENAI_API_KEY",
		},
		{
			name: "all env vars set",
			configContent: `
agent:
  name: "test"
provider:
  type: "openai"
  model: "gpt-4"
  api_key: "${OPENAI_API_KEY}"
channel:
  type: "telegram"
  token: "${TELEGRAM_TOKEN}"
store:
  type: "file"
  path: "/tmp"
`,
			setEnv: map[string]string{
				"OPENAI_API_KEY": "sk-test-123",
				"TELEGRAM_TOKEN": "test-token-456",
			},
			wantErr: false,
		},
		{
			name: "no env vars in config",
			configContent: `
agent:
  name: "test"
provider:
  type: "ollama"
  model: "llama3.2"
channel:
  type: "cli"
store:
  type: "file"
  path: "/tmp"
`,
			setEnv:  map[string]string{},
			wantErr: false,
		},
		{
			name: "multiple missing env vars",
			configContent: `
agent:
  name: "test"
provider:
  type: "openai"
  model: "gpt-4"
  api_key: "${OPENAI_API_KEY}"
channel:
  type: "discord"
  token: "${DISCORD_TOKEN}"
  app_id: "${DISCORD_APP_ID}"
store:
  type: "file"
  path: "/tmp"
`,
			setEnv: map[string]string{
				// None set
			},
			wantErr:     true,
			errContains: "OPENAI_API_KEY", // Should report all missing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			// Update path in config if needed
			configContent := strings.ReplaceAll(tt.configContent, "/tmp", tmpDir)

			if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
				t.Fatalf("Failed to write test config: %v", err)
			}

			// Set environment variables
			for k, v := range tt.setEnv {
				t.Setenv(k, v)
			}

			err := runDoctorCommand([]string{}, configPath)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestRunDoctorCommand_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		setupConfig func(t *testing.T) string // returns config path
		wantErr     bool
		errContains string // substring to check in error message
	}{
		{
			name: "valid config",
			setupConfig: func(t *testing.T) string {
				tmpDir := t.TempDir()
				configPath := filepath.Join(tmpDir, "config.yaml")
				configContent := `
agent:
  name: "test"
  max_iterations: 10
provider:
  type: "ollama"
  model: "llama3.2"
channel:
  type: "cli"
store:
  type: "file"
  path: "` + tmpDir + `"
`
				if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
					t.Fatalf("Failed to write test config: %v", err)
				}
				return configPath
			},
			wantErr: false,
		},
		{
			name: "missing config file with explicit path",
			setupConfig: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent.yaml")
			},
			wantErr:     true,
			errContains: "config file not found at",
		},
		{
			name: "invalid YAML syntax",
			setupConfig: func(t *testing.T) string {
				tmpDir := t.TempDir()
				configPath := filepath.Join(tmpDir, "config.yaml")
				// Invalid YAML - mismatched indentation / tab character causes parse error
				configContent := "agent:\n\tname: test"
				if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
					t.Fatalf("Failed to write test config: %v", err)
				}
				return configPath
			},
			wantErr:     true,
			errContains: "failed to load config",
		},
		{
			name: "empty config file",
			setupConfig: func(t *testing.T) string {
				tmpDir := t.TempDir()
				configPath := filepath.Join(tmpDir, "config.yaml")
				// Empty file
				if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
					t.Fatalf("Failed to write test config: %v", err)
				}
				return configPath
			},
			wantErr:     true,
			errContains: "failed to load config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := tt.setupConfig(t)
			err := runDoctorCommand([]string{}, configPath)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}
