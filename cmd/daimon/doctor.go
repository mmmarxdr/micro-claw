package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"daimon/internal/config"
)

// checkEnvVars parses config file content and checks which environment variables
// referenced in ${VAR} placeholders are set or missing.
func checkEnvVars(configContent string) ([]string, []string) {
	re := regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
	matches := re.FindAllStringSubmatch(configContent, -1)

	var setVars, missingVars []string
	seen := make(map[string]bool)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		varName := match[1]
		if seen[varName] {
			continue
		}
		seen[varName] = true

		if val, exists := os.LookupEnv(varName); exists && val != "" {
			setVars = append(setVars, varName)
		} else {
			missingVars = append(missingVars, varName)
		}
	}

	return setVars, missingVars
}

// checkStorePath validates that the store.path directory exists and is writable.
func checkStorePath(path string) error {
	if path == "" {
		return fmt.Errorf("store.path is empty")
	}

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("store.path %q does not exist", path)
		}
		return fmt.Errorf("failed to check store.path %q: %w", path, err)
	}

	// Check if it's a directory
	if !info.IsDir() {
		return fmt.Errorf("store.path %q is not a directory", path)
	}

	// Check if it's writable by trying to create a test file
	testFile := filepath.Join(path, ".microagent-doctor-test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("store.path %q is not writable: %w", path, err)
	}
	// Clean up test file
	os.Remove(testFile)

	return nil
}

// runDoctorCommand implements the "microagent doctor" subcommand.
// It loads the configuration from cfgPath and validates it.
func runDoctorCommand(args []string, cfgPath string) error {
	// Resolve config path (empty means use default search)
	resolvedPath, err := config.FindConfigPath(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to find config: %w", err)
	}

	// Read config file to check env vars
	configContent, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	setVars, missingVars := checkEnvVars(string(configContent))

	// Report env var status
	if len(missingVars) > 0 {
		return fmt.Errorf("missing environment variables: %s", strings.Join(missingVars, ", "))
	}

	// If all env vars are set, try to load config fully
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Check store path
	if cfg.Store.Type == "file" && cfg.Store.Path != "" {
		if err := checkStorePath(cfg.Store.Path); err != nil {
			return fmt.Errorf("store path validation failed: %w", err)
		}
		fmt.Printf("✓ Store path %q is accessible\n", cfg.Store.Path)
	}

	// Report success
	if len(setVars) > 0 {
		fmt.Printf("✓ All environment variables set: %s\n", strings.Join(setVars, ", "))
	} else {
		fmt.Println("✓ No environment variables referenced in config")
	}
	fmt.Printf("✓ Config loaded successfully from %s\n", resolvedPath)

	return nil
}
