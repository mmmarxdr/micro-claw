package main

import (
	"fmt"
	"os"

	"daimon/internal/setup"
)

// These variables allow tests to mock dependencies
var (
	isTTYFunc     = isTTY
	runWizardFunc = setup.RunWizard
)

// runSetupCommand implements the "microagent setup" subcommand.
// It checks if stdin is a TTY, runs the setup wizard, and exits.
// This mirrors the logic of the --setup flag in main.go.
func runSetupCommand(args []string, cfgPath string) error {
	if !isTTYFunc(os.Stdin) {
		return fmt.Errorf("Setup wizard requires an interactive terminal.")
	}
	if _, err := runWizardFunc(); err != nil {
		return fmt.Errorf("Setup wizard failed: %w", err)
	}
	return nil
}
