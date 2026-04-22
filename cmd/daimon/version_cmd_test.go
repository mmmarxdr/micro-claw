package main

import (
	"strings"
	"testing"
)

func TestRunVersionCommand_Default(t *testing.T) {
	origVersion, origCommit, origDate := version, commit, date
	defer func() { version, commit, date = origVersion, origCommit, origDate }()
	version, commit, date = "v9.9.9", "deadbeef", "2026-01-01"

	out := captureStdout(t, func() {
		if err := runVersionCommand(nil); err != nil {
			t.Fatalf("runVersionCommand: %v", err)
		}
	})
	if !strings.Contains(out, "daimon v9.9.9 (deadbeef, 2026-01-01)") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestRunVersionCommand_Short(t *testing.T) {
	origVersion := version
	defer func() { version = origVersion }()
	version = "v9.9.9"

	out := captureStdout(t, func() {
		if err := runVersionCommand([]string{"--short"}); err != nil {
			t.Fatalf("runVersionCommand: %v", err)
		}
	})
	if strings.TrimSpace(out) != "v9.9.9" {
		t.Errorf("expected just version, got %q", out)
	}
}
