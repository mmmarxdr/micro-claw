package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"microagent/internal/config"
)

// --- helpers ---

// newConfigTestServer returns a Server wired with a writable config path.
// cfgPath may be empty for tests that do not write to disk.
func newConfigTestServer(cfg *config.Config, cfgPath string) *Server {
	return newSetupTestServerWithConfigPath(cfg, cfgPath)
}

// authHeader returns an Authorization header value for the given token.
func authHeader(token string) string { return "Bearer " + token }

// --- T-38: Auth guard ---

// TestHandlePutConfig_NoAuth verifies that PUT /api/config returns 401
// when no auth cookie and no Authorization header is provided.
func TestHandlePutConfig_NoAuth(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	s := newConfigTestServer(cfg, "")

	body := []byte(`{"providers":{"anthropic":{"api_key":"new-key"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately omit Authorization and cookie.
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// --- T-39: Validation helper tests ---

// TestHandlePutConfig_UnknownProvider verifies AS-13: unknown provider name → 400.
func TestHandlePutConfig_UnknownProvider(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	body := []byte(`{"providers":{"grok-unknown":{"api_key":"x"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error message")
	}
}

// TestHandlePutConfig_MaskedStripped verifies AS-12: masked api_key in body is stripped;
// the stored key is preserved unchanged.
func TestHandlePutConfig_MaskedStripped(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	const realKey = "sk-or-v1-realrealrealkey"
	cfg.Providers["openrouter"] = config.ProviderCredentials{APIKey: realKey}
	cfg.Models.Default.Provider = "anthropic"
	cfg.Models.Default.Model = "claude-test"

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	// Send a masked value for openrouter — it must be treated as absent.
	body := []byte(`{"providers":{"openrouter":{"api_key":"sk-o****a68c"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// The in-memory config must still have the real key.
	got := s.deps.Config.Providers["openrouter"].APIKey
	if got != realKey {
		t.Errorf("stored key mutated: want %q got %q", realKey, got)
	}
}

// TestHandlePutConfig_PartialUpdate verifies AS-11: partial update preserves existing providers.
func TestHandlePutConfig_PartialUpdate(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	const orKey = "real-or-key"
	cfg.Providers["openrouter"] = config.ProviderCredentials{APIKey: orKey}
	// Active provider is anthropic (already in minimalConfig).

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	// Only update anthropic key — openrouter must be preserved.
	body := []byte(`{"providers":{"anthropic":{"api_key":"sk-ant-test"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := s.deps.Config.Providers["openrouter"].APIKey; got != orKey {
		t.Errorf("openrouter key should be preserved: want %q got %q", orKey, got)
	}
	if got := s.deps.Config.Providers["anthropic"].APIKey; got != "sk-ant-test" {
		t.Errorf("anthropic key should be updated: got %q", got)
	}
}

// TestHandlePutConfig_ModelsDefaultUpdate verifies AS-15: models.default update succeeds
// when the target provider already has credentials.
func TestHandlePutConfig_ModelsDefaultUpdate(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	cfg.Providers["anthropic"] = config.ProviderCredentials{APIKey: "sk-ant-existing"}

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	body := []byte(`{"models":{"default":{"provider":"anthropic","model":"claude-opus-4-6"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := s.deps.Config.Models.Default.Provider; got != "anthropic" {
		t.Errorf("expected provider=anthropic, got %q", got)
	}
	if got := s.deps.Config.Models.Default.Model; got != "claude-opus-4-6" {
		t.Errorf("expected model=claude-opus-4-6, got %q", got)
	}
}

// TestHandlePutConfig_InconsistentState verifies AS-16: setting active provider to one
// with no credentials → 400.
func TestHandlePutConfig_InconsistentState(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	// No gemini credentials in config.

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	body := []byte(`{"models":{"default":{"provider":"gemini","model":"gemini-pro"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp) //nolint:errcheck
	if resp["error"] == "" {
		t.Error("expected non-empty error in body")
	}
}

// --- T-44: GET masking expansion ---

// TestHandleGetConfig_AllKeysMasked verifies AS-17: all sensitive fields are masked in GET response.
func TestHandleGetConfig_AllKeysMasked(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token-get"
	cfg.Providers = map[string]config.ProviderCredentials{
		"anthropic":  {APIKey: "sk-ant-realkey1234"},
		"openrouter": {APIKey: "sk-or-realkey5678"},
	}
	cfg.Models = config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-test"}}
	cfg.Channel.Token = "channel-real-token"
	cfg.Web.AuthToken = "web-auth-real-token"

	s := newConfigTestServer(cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Authorization", authHeader("web-auth-real-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// None of the real keys must appear verbatim.
	realSecrets := []string{
		"sk-ant-realkey1234",
		"sk-or-realkey5678",
		"channel-real-token",
		"web-auth-real-token",
	}
	for _, secret := range realSecrets {
		if contains(body, secret) {
			t.Errorf("real secret %q leaked in GET /api/config response", secret)
		}
	}

	// Each masked value must match MaskedPattern.
	var respCfg struct {
		Providers map[string]struct {
			APIKey string `json:"api_key"`
		} `json:"providers"`
		Channel struct {
			Token string `json:"token"`
		} `json:"channel"`
		Web struct {
			AuthToken string `json:"auth_token"`
		} `json:"web"`
	}
	if err := json.Unmarshal([]byte(body), &respCfg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for name, creds := range respCfg.Providers {
		if creds.APIKey != "" && !config.IsMasked(creds.APIKey) {
			t.Errorf("providers[%s].api_key %q is not masked", name, creds.APIKey)
		}
	}
	if respCfg.Channel.Token != "" && !config.IsMasked(respCfg.Channel.Token) {
		t.Errorf("channel.token %q is not masked", respCfg.Channel.Token)
	}
	if respCfg.Web.AuthToken != "" && !config.IsMasked(respCfg.Web.AuthToken) {
		t.Errorf("web.auth_token %q is not masked", respCfg.Web.AuthToken)
	}
}

// TestHandlePutConfig_InvalidJSON verifies that malformed JSON returns 400.
func TestHandlePutConfig_InvalidJSON(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	s := newConfigTestServer(cfg, "")

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader([]byte(`{not-valid-json`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// TestHandlePutConfig_OllamaNoAPIKey verifies that ollama without api_key passes validation.
func TestHandlePutConfig_OllamaNoAPIKey(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	cfg.Providers["ollama"] = config.ProviderCredentials{BaseURL: "http://localhost:11434"}

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	s := newConfigTestServer(cfg, cfgPath)

	body := []byte(`{"models":{"default":{"provider":"ollama","model":"llama3"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for ollama (no key required), got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleGetConfig_FallbackMasked verifies that fallback.api_key is masked in GET response.
func TestHandleGetConfig_FallbackMasked(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	cfg.Fallback = &config.FallbackConfig{APIKey: "fallback-real-key"}

	s := newConfigTestServer(cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if contains(rec.Body.String(), "fallback-real-key") {
		t.Error("fallback.api_key real value leaked in GET response")
	}
}

// contains is a simple substring check for test assertions.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
