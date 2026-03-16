package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileAuditor_WritesJSONL(t *testing.T) {
	dir := t.TempDir()
	a, err := NewFileAuditor(dir)
	if err != nil {
		t.Fatalf("NewFileAuditor: %v", err)
	}
	defer a.Close()

	events := []AuditEvent{
		{ID: "1", ScopeID: "cli", EventType: "llm_call", Timestamp: time.Now(), InputTokens: 100, OutputTokens: 50},
		{ID: "2", ScopeID: "cli", EventType: "tool_use", Timestamp: time.Now(), ToolName: "shell_exec", ToolOK: true},
	}
	for _, e := range events {
		if err := a.Emit(context.Background(), e); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	a.Close()

	path := filepath.Join(dir, "audit_cli.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("expected audit file at %q: %v", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for i, l := range lines {
		var ev AuditEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestFileAuditor_ScopeIsolation(t *testing.T) {
	dir := t.TempDir()
	a, err := NewFileAuditor(dir)
	if err != nil {
		t.Fatalf("NewFileAuditor: %v", err)
	}
	defer a.Close()

	_ = a.Emit(context.Background(), AuditEvent{ID: "a", ScopeID: "cli", EventType: "llm_call"})
	_ = a.Emit(context.Background(), AuditEvent{ID: "b", ScopeID: "telegram:12345", EventType: "tool_use"})

	a.Close()

	if _, err := os.Stat(filepath.Join(dir, "audit_cli.jsonl")); err != nil {
		t.Errorf("expected audit_cli.jsonl: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit_telegram_12345.jsonl")); err != nil {
		t.Errorf("expected audit_telegram_12345.jsonl: %v", err)
	}
}

func TestNoopAuditor(t *testing.T) {
	var a NoopAuditor
	if err := a.Emit(context.Background(), AuditEvent{}); err != nil {
		t.Errorf("NoopAuditor.Emit returned error: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("NoopAuditor.Close returned error: %v", err)
	}
}
