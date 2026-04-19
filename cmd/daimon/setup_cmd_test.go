package main

import (
	"errors"
	"os"
	"testing"
)

// TestRunSetupCommand_Success tests that runSetupCommand returns nil when
// setup.RunWizard succeeds.
func TestRunSetupCommand_Success(t *testing.T) {
	// Save original functions
	origIsTTY := isTTYFunc
	origRunWizard := runWizardFunc
	defer func() {
		isTTYFunc = origIsTTY
		runWizardFunc = origRunWizard
	}()

	// Mock isTTY to return true
	isTTYFunc = func(f *os.File) bool {
		return true
	}

	// Mock RunWizard to succeed
	runWizardFunc = func() (string, error) {
		return "/tmp/config.yaml", nil
	}

	err := runSetupCommand([]string{}, "")
	if err != nil {
		t.Errorf("runSetupCommand returned error: %v", err)
	}
}

// TestRunSetupCommand_NonTTY tests that runSetupCommand returns error when
// not in a TTY.
func TestRunSetupCommand_NonTTY(t *testing.T) {
	// Save original functions
	origIsTTY := isTTYFunc
	defer func() {
		isTTYFunc = origIsTTY
	}()

	// Mock isTTY to return false
	isTTYFunc = func(f *os.File) bool {
		return false
	}

	err := runSetupCommand([]string{}, "")
	if err == nil {
		t.Error("runSetupCommand should return error when not in TTY")
	}
	expectedMsg := "Setup wizard requires an interactive terminal."
	if err.Error() != expectedMsg {
		t.Errorf("expected error %q, got %q", expectedMsg, err.Error())
	}
}

// TestRunSetupCommand_WizardError tests that runSetupCommand returns error when
// setup.RunWizard fails.
func TestRunSetupCommand_WizardError(t *testing.T) {
	// Save original functions
	origIsTTY := isTTYFunc
	origRunWizard := runWizardFunc
	defer func() {
		isTTYFunc = origIsTTY
		runWizardFunc = origRunWizard
	}()

	// Mock isTTY to return true
	isTTYFunc = func(f *os.File) bool {
		return true
	}

	// Mock RunWizard to fail
	runWizardFunc = func() (string, error) {
		return "", errors.New("wizard failed")
	}

	err := runSetupCommand([]string{}, "")
	if err == nil {
		t.Error("runSetupCommand should return error when wizard fails")
	}
	expectedPrefix := "Setup wizard failed:"
	if err.Error()[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("expected error starting with %q, got %q", expectedPrefix, err.Error())
	}
}
