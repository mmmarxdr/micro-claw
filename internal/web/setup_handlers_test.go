package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daimon/internal/config"
	"daimon/internal/provider"
)

// --- T02: GET /api/setup/status ---

func TestHandleSetupStatus_ConfigComplete(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{
		"anthropic": {APIKey: "sk-ant-xxx"},
	}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}}
	cfg.Web.AuthToken = "test-token"

	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	// No Authorization header — must still return 200.
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		NeedsSetup bool     `json:"needs_setup"`
		Missing    []string `json:"missing"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.NeedsSetup {
		t.Errorf("expected needs_setup=false, got true")
	}
	if len(resp.Missing) != 0 {
		t.Errorf("expected no missing fields, got %v", resp.Missing)
	}
}

func TestHandleSetupStatus_ConfigMissing(t *testing.T) {
	cfg := minimalConfig()
	// Clear providers and models — needs setup
	cfg.Providers = nil
	cfg.Models = config.ModelsConfig{}
	cfg.Web.AuthToken = "test-token"

	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		NeedsSetup bool     `json:"needs_setup"`
		Missing    []string `json:"missing"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !resp.NeedsSetup {
		t.Errorf("expected needs_setup=true, got false")
	}
	if len(resp.Missing) == 0 {
		t.Errorf("expected missing fields, got none")
	}
}

func TestHandleSetupStatus_BypassesAuth(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{
		"anthropic": {APIKey: "sk-ant-xxx"},
	}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}}
	cfg.Web.AuthToken = "secret-token"

	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	// Deliberately no Authorization header.
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("setup/status must bypass auth, got 401")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- T03: GET /api/setup/providers ---

func TestHandleSetupProviders_ReturnsAllProviders(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "test-token"
	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/providers", nil)
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" {
		t.Error("Content-Type not set")
	}

	var resp struct {
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	for _, key := range []string{"anthropic", "gemini", "openai", "openrouter", "ollama"} {
		if _, ok := resp.Providers[key]; !ok {
			t.Errorf("missing provider key %q in response", key)
		}
	}
}

func TestHandleSetupProviders_OllamaIsEmptyModels(t *testing.T) {
	cfg := minimalConfig()
	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/providers", nil)
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	var resp struct {
		Providers map[string]struct {
			DisplayName    string            `json:"display_name"`
			RequiresAPIKey bool              `json:"requires_api_key"`
			Models         []json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	ollama, ok := resp.Providers["ollama"]
	if !ok {
		t.Fatal("missing ollama key")
	}
	if ollama.RequiresAPIKey {
		t.Error("ollama should not require API key")
	}
	if len(ollama.Models) != 0 {
		t.Errorf("expected empty ollama models, got %d entries", len(ollama.Models))
	}
}

func TestHandleSetupProviders_NoSentinelEntries(t *testing.T) {
	cfg := minimalConfig()
	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/providers", nil)
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	var resp struct {
		Providers map[string]struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	for provider, info := range resp.Providers {
		for _, m := range info.Models {
			if m.ID == "" {
				t.Errorf("provider %q has a sentinel entry with empty id", provider)
			}
		}
	}
}

func TestHandleSetupProviders_BypassesAuth(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	s := newSetupTestServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/providers", nil)
	// No auth header.
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("setup/providers must bypass auth, got 401")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// --- T05: POST /api/setup/validate-key ---

func TestHandleValidateKey_ValidKey(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = nil
	cfg.Models = config.ModelsConfig{}
	cfg.Web.AuthToken = "test-token"
	s := newSetupTestServerWithFactory(cfg, mockProviderFactory(nil))

	body := `{"provider":"anthropic","api_key":"sk-ant-valid","model":"claude-sonnet-4-6","base_url":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/validate-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp validateKeyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !resp.Valid {
		t.Errorf("expected valid=true, got false: %s", resp.Error)
	}
}

func TestHandleValidateKey_InvalidKey(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = nil
	cfg.Models = config.ModelsConfig{}
	s := newSetupTestServerWithFactory(cfg, mockProviderFactory(provider.ErrAuth))

	body := `{"provider":"anthropic","api_key":"sk-bad","model":"claude-sonnet-4-6","base_url":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/validate-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp validateKeyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Valid {
		t.Errorf("expected valid=false, got true")
	}
	if resp.Error == "" {
		t.Errorf("expected error message, got empty string")
	}
}

func TestHandleValidateKey_NonJSONBody(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = nil
	cfg.Models = config.ModelsConfig{}
	s := newSetupTestServerWithFactory(cfg, mockProviderFactory(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/setup/validate-key", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleValidateKey_BypassesAuth(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	s := newSetupTestServerWithFactory(cfg, mockProviderFactory(nil))

	body := `{"provider":"anthropic","api_key":"sk-ant-valid","model":"claude-sonnet-4-6"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/validate-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Errorf("validate-key must bypass auth, got 401")
	}
}

func TestHandleValidateKey_SetupAlreadyComplete_Returns403(t *testing.T) {
	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{
		"anthropic": {APIKey: "sk-ant-existing"},
	}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}}
	s := newSetupTestServerWithFactory(cfg, mockProviderFactory(nil))

	body := `{"provider":"anthropic","api_key":"sk-ant-valid","model":"claude-sonnet-4-6"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/validate-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when setup already complete, got %d", w.Code)
	}
}

// --- T06: POST /api/setup/complete ---

func TestHandleSetupComplete_FirstTime(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	// Not configured yet — no providers/models
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"provider":"anthropic","api_key":"sk-ant-test","model":"claude-sonnet-4-6","base_url":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp setupCompleteResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success=true")
	}
	if resp.AuthToken == "" {
		t.Errorf("expected non-empty auth_token")
	}
	if resp.ConfigPath == "" {
		t.Errorf("expected non-empty config_path")
	}

	// Config file must exist on disk.
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestHandleSetupComplete_PreservesExistingFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Write an existing config with a custom agent name.
	existing := `agent:
  name: my-existing-agent
provider:
  type: openai
  model: gpt-4o
  api_key: sk-old
web:
  enabled: true
  port: 9090
  auth_token: existing-token
`
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{"openai": {APIKey: "sk-old"}}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "openai", Model: "gpt-4o"}}
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"provider":"anthropic","api_key":"sk-ant-new","model":"claude-sonnet-4-6"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read back and verify non-provider fields preserved.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "my-existing-agent") {
		t.Errorf("agent name was overwritten: %s", data)
	}
	if !strings.Contains(string(data), "anthropic") {
		t.Errorf("new provider type not written: %s", data)
	}
}

func TestHandleSetupComplete_OllamaNoKeyRequired(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	// Not configured yet
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"provider":"ollama","api_key":"","model":"llama3","base_url":"http://localhost:11434"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for Ollama without key, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleSetupComplete_MissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	// Not configured yet
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	// Missing model.
	body := `{"provider":"anthropic","api_key":"sk-ant-test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model, got %d", w.Code)
	}
}

func TestHandleSetupComplete_ProviderTypeChange_RestartRequired(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{"openai": {APIKey: "sk-old"}}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "openai", Model: "gpt-4o"}}
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"provider":"anthropic","api_key":"sk-ant-new","model":"claude-sonnet-4-6"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/complete", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp setupCompleteResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !resp.RestartRequired {
		t.Errorf("expected restart_required=true when provider type changes")
	}
}

// --- T07: POST /api/setup/reset ---

func TestHandleSetupReset_Confirmed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	existing := `agent:
  name: my-agent
provider:
  type: anthropic
  model: claude-sonnet-4-6
  api_key: sk-ant-test
web:
  enabled: true
  auth_token: existing-token
`
	if err := os.WriteFile(cfgPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	cfg := minimalConfig()
	cfg.Providers = map[string]config.ProviderCredentials{"anthropic": {APIKey: "sk-ant-test"}}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-sonnet-4-6"}}
	cfg.Web.AuthToken = "existing-token"
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"confirm":"DELETE"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer existing-token")
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if success, _ := resp["success"].(bool); !success {
		t.Errorf("expected success=true")
	}

	// Verify provider fields cleared in config file.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "sk-ant-test") {
		t.Errorf("api_key should have been cleared: %s", data)
	}
	// Non-provider fields preserved.
	if !strings.Contains(string(data), "my-agent") {
		t.Errorf("agent name should be preserved: %s", data)
	}
}

func TestHandleSetupReset_WrongConfirmation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Web.AuthToken = "tok"
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	for _, confirm := range []string{"delete", "yes", "CONFIRM", ""} {
		body := fmt.Sprintf(`{"confirm":%q}`, confirm)
		req := httptest.NewRequest(http.MethodPost, "/api/setup/reset", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		s.srv.Handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("confirm=%q: expected 400, got %d", confirm, w.Code)
		}
	}
}

func TestHandleSetupReset_RequiresAuth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	s := newSetupTestServerWithConfigPath(cfg, cfgPath)

	body := `{"confirm":"DELETE"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup/reset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.
	w := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}
