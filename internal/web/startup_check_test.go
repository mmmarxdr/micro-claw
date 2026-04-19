package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"daimon/internal/config"
	"daimon/internal/provider"
)

// captureSlog swaps the default logger for a JSON handler writing to a buffer,
// returning the buffer + cleanup. Use JSON so the message field can be inspected
// without worrying about TextHandler's inner-quote escaping.
func captureSlog(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf, func() { slog.SetDefault(prev) }
}

// lastLogMsg extracts the `msg` field from the last JSON log line in buf.
func lastLogMsg(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("no log lines captured")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("parse log line: %v (line=%s)", err, lines[len(lines)-1])
	}
	msg, _ := entry["msg"].(string)
	return msg
}

// fakeModelLister is a ModelLister that returns canned results for startup tests.
type fakeModelLister struct {
	models []provider.ModelInfo
	err    error
}

func (f *fakeModelLister) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	return f.models, f.err
}

// fakeValidationRegistry wraps listers by name.
type fakeValidationRegistry struct {
	listers map[string]provider.ModelLister
}

func (r *fakeValidationRegistry) Lister(name string) (provider.ModelLister, bool) {
	ml, ok := r.listers[name]
	return ml, ok
}

// 9.1.1 — configured model not in live list → warning logged, no error returned
func TestValidateConfiguredModel_ModelNotFound_ReturnsNil(t *testing.T) {
	reg := &fakeValidationRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeModelLister{
				models: []provider.ModelInfo{
					{ID: "claude-sonnet-4-6"},
					{ID: "claude-haiku-4-5"},
				},
			},
		},
	}
	cfg := config.Config{}
	cfg.Models.Default.Provider = "anthropic"
	cfg.Models.Default.Model = "claude-opus-x-99" // not in list

	buf, restore := captureSlog(t)
	defer restore()

	// Must not return an error — only a warning.
	err := validateConfiguredModel(context.Background(), reg, cfg)
	if err != nil {
		t.Errorf("expected nil error (warn only), got %v", err)
	}

	// REQ-PMD-9 exact format: [daimon] WARNING: model "X" not found in provider "Y" live list — run daimon config to update
	msg := lastLogMsg(t, buf)
	wantSubstrs := []string{
		`[daimon] WARNING`,
		`model "claude-opus-x-99"`,
		`not found in provider "anthropic" live list`,
		`run daimon config to update`,
	}
	for _, s := range wantSubstrs {
		if !strings.Contains(msg, s) {
			t.Errorf("REQ-PMD-9 log format drift: expected msg to contain %q\n  got msg: %q", s, msg)
		}
	}
}

// 9.1.2 — ListModels itself errors → validation skips silently, no error returned.
func TestValidateConfiguredModel_ListModelsFails_ReturnsNil(t *testing.T) {
	reg := &fakeValidationRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeModelLister{
				err: errors.New("network timeout"),
			},
		},
	}
	cfg := config.Config{}
	cfg.Models.Default.Provider = "anthropic"
	cfg.Models.Default.Model = "claude-sonnet-4-6"

	err := validateConfiguredModel(context.Background(), reg, cfg)
	if err != nil {
		t.Errorf("expected nil on ListModels failure, got %v", err)
	}
}

// 9.1.3 — valid model → no warning, no error.
func TestValidateConfiguredModel_ValidModel_ReturnsNil(t *testing.T) {
	reg := &fakeValidationRegistry{
		listers: map[string]provider.ModelLister{
			"anthropic": &fakeModelLister{
				models: []provider.ModelInfo{
					{ID: "claude-sonnet-4-6"},
				},
			},
		},
	}
	cfg := config.Config{}
	cfg.Models.Default.Provider = "anthropic"
	cfg.Models.Default.Model = "claude-sonnet-4-6"

	err := validateConfiguredModel(context.Background(), reg, cfg)
	if err != nil {
		t.Errorf("expected nil for valid model, got %v", err)
	}
}

// 9.1.4 — provider not in registry (no credentials) → skip validation, no error.
func TestValidateConfiguredModel_ProviderNotInRegistry_ReturnsNil(t *testing.T) {
	reg := &fakeValidationRegistry{
		listers: map[string]provider.ModelLister{},
	}
	cfg := config.Config{}
	cfg.Models.Default.Provider = "openrouter"
	cfg.Models.Default.Model = "some-model"

	err := validateConfiguredModel(context.Background(), reg, cfg)
	if err != nil {
		t.Errorf("expected nil when provider absent from registry, got %v", err)
	}
}
