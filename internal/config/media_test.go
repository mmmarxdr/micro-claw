package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// minimalYAML returns a valid base YAML string with no media section.
// Tests append their own media block on top of this.
const minimalYAML = `
provider:
  type: "test_provider"
  model: "test-model"
  api_key: "test-key"
`

// loadMediaConfig is a helper that constructs a full YAML by appending extra
// to the minimal base, writes a temp file, and calls Load.
func loadMediaConfig(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	data := minimalYAML + extra
	f := createTempFile(t, data)
	defer os.Remove(f)
	return Load(f)
}

func TestMediaConfig_DefaultsAppliedWhenAbsent(t *testing.T) {
	cfg, err := loadMediaConfig(t, "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	m := cfg.Media
	if !BoolVal(m.Enabled) {
		t.Error("Media.Enabled: want true, got false")
	}
	if m.MaxAttachmentBytes != 10485760 {
		t.Errorf("MaxAttachmentBytes: want 10485760, got %d", m.MaxAttachmentBytes)
	}
	if m.MaxMessageBytes != 26214400 {
		t.Errorf("MaxMessageBytes: want 26214400, got %d", m.MaxMessageBytes)
	}
	if m.RetentionDays != 30 {
		t.Errorf("RetentionDays: want 30, got %d", m.RetentionDays)
	}
	if m.CleanupInterval != 24*time.Hour {
		t.Errorf("CleanupInterval: want 24h, got %s", m.CleanupInterval)
	}
	want := []string{"image/", "audio/", "application/pdf", "text/"}
	if len(m.AllowedMIMEPrefixes) != len(want) {
		t.Errorf("AllowedMIMEPrefixes len: want %d, got %d", len(want), len(m.AllowedMIMEPrefixes))
	} else {
		for i, p := range want {
			if m.AllowedMIMEPrefixes[i] != p {
				t.Errorf("AllowedMIMEPrefixes[%d]: want %q, got %q", i, p, m.AllowedMIMEPrefixes[i])
			}
		}
	}
}

func TestMediaConfig_ValidationMaxAttachmentTooSmall(t *testing.T) {
	_, err := loadMediaConfig(t, `
media:
  enabled: true
  max_attachment_bytes: 512
  max_message_bytes: 26214400
  retention_days: 30
  cleanup_interval: 24h
  allowed_mime_prefixes: ["image/"]
`)
	if err == nil {
		t.Fatal("expected validation error for max_attachment_bytes < 1024, got nil")
	}
	if !strings.Contains(err.Error(), "max_attachment_bytes") {
		t.Errorf("error should mention max_attachment_bytes, got: %v", err)
	}
}

func TestMediaConfig_ValidationMaxAttachmentTooLarge(t *testing.T) {
	_, err := loadMediaConfig(t, `
media:
  enabled: true
  max_attachment_bytes: 52428801
  max_message_bytes: 104857600
  retention_days: 30
  cleanup_interval: 24h
  allowed_mime_prefixes: ["image/"]
`)
	if err == nil {
		t.Fatal("expected validation error for max_attachment_bytes > 52428800, got nil")
	}
	if !strings.Contains(err.Error(), "max_attachment_bytes") {
		t.Errorf("error should mention max_attachment_bytes, got: %v", err)
	}
}

func TestMediaConfig_ValidationMaxMessageLessThanAttachment(t *testing.T) {
	_, err := loadMediaConfig(t, `
media:
  enabled: true
  max_attachment_bytes: 5242880
  max_message_bytes: 1048576
  retention_days: 30
  cleanup_interval: 24h
  allowed_mime_prefixes: ["image/"]
`)
	if err == nil {
		t.Fatal("expected validation error for max_message_bytes < max_attachment_bytes, got nil")
	}
	if !strings.Contains(err.Error(), "max_message_bytes") {
		t.Errorf("error should mention max_message_bytes, got: %v", err)
	}
}

func TestMediaConfig_ValidationRetentionDaysZero(t *testing.T) {
	// retention_days: 0 is the zero value and gets replaced by the default (30).
	// To trigger the < 1 validation we must use a negative value, which survives defaults.
	_, err := loadMediaConfig(t, `
media:
  enabled: true
  max_attachment_bytes: 10485760
  max_message_bytes: 26214400
  retention_days: -1
  cleanup_interval: 24h
  allowed_mime_prefixes: ["image/"]
`)
	if err == nil {
		t.Fatal("expected validation error for retention_days < 1, got nil")
	}
	if !strings.Contains(err.Error(), "retention_days") {
		t.Errorf("error should mention retention_days, got: %v", err)
	}
}

func TestMediaConfig_ValidationEmptyMIMEPrefixes(t *testing.T) {
	// allowed_mime_prefixes: [] is the zero/empty value and gets replaced by defaults
	// during applyDefaults. To test the validator path we build a valid Config
	// directly, apply defaults, then clear the slice (simulating programmatic mutation).
	cfg := &Config{
		Provider: ProviderConfig{Type: "test_provider", APIKey: "key", Model: "m"},
		Channel:  ChannelConfig{Type: "cli"},
	}
	cfg.ApplyDefaults()
	// Defaults should have enabled media.
	if !BoolVal(cfg.Media.Enabled) {
		t.Fatal("expected Media.Enabled to default to true")
	}
	// Force empty to trigger the validator.
	cfg.Media.AllowedMIMEPrefixes = []string{}

	err := cfg.validate()
	if err == nil {
		t.Fatal("expected validation error for empty allowed_mime_prefixes, got nil")
	}
	if !strings.Contains(err.Error(), "allowed_mime_prefixes") {
		t.Errorf("error should mention allowed_mime_prefixes, got: %v", err)
	}
}

func TestMediaConfig_ValidationCleanupIntervalTooShort(t *testing.T) {
	_, err := loadMediaConfig(t, `
media:
  enabled: true
  max_attachment_bytes: 10485760
  max_message_bytes: 26214400
  retention_days: 30
  cleanup_interval: 30m
  allowed_mime_prefixes: ["image/"]
`)
	if err == nil {
		t.Fatal("expected validation error for cleanup_interval < 1h, got nil")
	}
	if !strings.Contains(err.Error(), "cleanup_interval") {
		t.Errorf("error should mention cleanup_interval, got: %v", err)
	}
}

func TestMediaConfig_DisabledSkipsValidation(t *testing.T) {
	// When enabled=false, zero-value fields must not produce any validation error.
	cfg, err := loadMediaConfig(t, `
media:
  enabled: false
`)
	if err != nil {
		t.Fatalf("expected no error when media.enabled=false, got: %v", err)
	}
	if BoolVal(cfg.Media.Enabled) {
		t.Error("Media.Enabled should be false")
	}
}
