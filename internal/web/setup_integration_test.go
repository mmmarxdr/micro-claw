package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"microagent/internal/config"
)

// newSetupIntegrationServer creates a full Server (with auth middleware) backed by a
// temp config directory. It uses a mock provider factory so no live API calls are made.
func newSetupIntegrationServer(t *testing.T, cfg *config.Config, cfgPath string) (*Server, *httptest.Server) {
	t.Helper()
	deps := ServerDeps{
		Config:          cfg,
		ConfigPath:      cfgPath,
		ProviderFactory: mockProviderFactory(nil),
	}
	s := NewServer(deps)
	ts := httptest.NewServer(s.srv.Handler)
	t.Cleanup(ts.Close)
	return s, ts
}

// doJSON sends a JSON request and returns the response.
func doJSON(t *testing.T, client *http.Client, method, url string, body any, authToken string) *http.Response {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request %s %s: %v", method, url, err)
	}
	return resp
}

// decodeJSON decodes a response body into a map and closes the body.
func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	return result
}

// TestIntegration_SetupFlow_HappyPath exercises the full setup flow end-to-end.
func TestIntegration_SetupFlow_HappyPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := &config.Config{
		Agent:   config.AgentConfig{Name: "test-agent"},
		Channel: config.ChannelConfig{Type: "cli"},
		Web:     config.WebConfig{Host: "127.0.0.1", Port: 8080},
		// No provider — needs setup.
	}

	_, ts := newSetupIntegrationServer(t, cfg, cfgPath)
	client := ts.Client()

	// Step 1: GET /api/setup/status → needs_setup: true (no provider configured).
	t.Run("step1_status_needs_setup", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, ts.URL+"/api/setup/status", nil, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if needsSetup, _ := body["needs_setup"].(bool); !needsSetup {
			t.Errorf("expected needs_setup=true, got %v", body["needs_setup"])
		}
	})

	// Step 2: GET /api/setup/providers → returns provider list with all 5 keys.
	t.Run("step2_providers", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, ts.URL+"/api/setup/providers", nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		defer resp.Body.Close()

		var wrapper struct {
			Providers map[string]json.RawMessage `json:"providers"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
			t.Fatalf("decode providers: %v", err)
		}

		for _, key := range []string{"anthropic", "gemini", "openai", "openrouter", "ollama"} {
			if _, ok := wrapper.Providers[key]; !ok {
				t.Errorf("missing provider key %q", key)
			}
		}
	})

	// Step 3: POST /api/setup/validate-key → valid: true (mock provider).
	t.Run("step3_validate_key", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/validate-key", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test",
			"model":    "claude-sonnet-4-6",
		}, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if valid, _ := body["valid"].(bool); !valid {
			t.Errorf("expected valid=true, got %v", body["valid"])
		}
	})

	// Step 4: POST /api/setup/complete → returns auth_token.
	var authToken string
	t.Run("step4_complete", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/complete", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test",
			"model":    "claude-sonnet-4-6",
		}, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
		}
		if success, _ := body["success"].(bool); !success {
			t.Errorf("expected success=true, got %v", body["success"])
		}
		tok, _ := body["auth_token"].(string)
		if tok == "" {
			t.Fatal("expected non-empty auth_token")
		}
		authToken = tok

		// Config file must be on disk.
		if _, err := os.Stat(cfgPath); err != nil {
			t.Errorf("config file not created: %v", err)
		}
	})

	// Step 5: GET /api/setup/status → needs_setup: false (provider now configured).
	t.Run("step5_status_configured", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, ts.URL+"/api/setup/status", nil, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if needsSetup, _ := body["needs_setup"].(bool); needsSetup {
			t.Errorf("expected needs_setup=false after setup complete, got true")
		}
	})

	// Step 6: POST /api/setup/validate-key → 403 (locked after setup complete).
	t.Run("step6_validate_key_locked", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/validate-key", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test",
			"model":    "claude-sonnet-4-6",
		}, "")
		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("expected 403 after setup complete, got %d", resp.StatusCode)
		}
	})

	// Step 7: POST /api/setup/complete → still succeeds (no lock on complete endpoint).
	// The spec only locks validate-key; complete may be called again to reconfigure.
	t.Run("step7_complete_still_accessible", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/complete", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test2",
			"model":    "claude-sonnet-4-6",
		}, "")
		resp.Body.Close()

		// complete is always open (reconfigure path), not locked like validate-key.
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 on re-complete, got %d", resp.StatusCode)
		}
	})

	// Prevent unused variable warning if step4 is skipped.
	_ = authToken
}

// TestIntegration_ResetFlow exercises the full reset flow after setup.
func TestIntegration_ResetFlow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Auth token must be pre-set so the auth middleware is active.
	// setup/complete will preserve it (existing token kept).
	const presetToken = "preset-test-token"

	cfg := &config.Config{
		Agent:   config.AgentConfig{Name: "test-agent"},
		Channel: config.ChannelConfig{Type: "cli"},
		Web: config.WebConfig{
			Host:      "127.0.0.1",
			Port:      8080,
			AuthToken: presetToken,
		},
	}

	_, ts := newSetupIntegrationServer(t, cfg, cfgPath)
	client := ts.Client()

	// Write a pre-existing config file containing the preset token so that
	// setup/complete preserves it (existing token is never overwritten).
	existingYAML := "web:\n  auth_token: " + presetToken + "\n"
	if err := os.WriteFile(cfgPath, []byte(existingYAML), 0o600); err != nil {
		t.Fatalf("write pre-existing config: %v", err)
	}

	// Perform initial setup; complete preserves the preset token.
	resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/complete", map[string]string{
		"provider": "anthropic",
		"api_key":  "sk-ant-reset-test",
		"model":    "claude-sonnet-4-6",
	}, "")
	setupBody := decodeJSON(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup complete failed: %d %v", resp.StatusCode, setupBody)
	}

	// The auth token used by the middleware is presetToken (captured at NewServer time).
	// We use it directly rather than relying on the returned token.
	authToken := presetToken

	// Verify setup is done.
	statusResp := doJSON(t, client, http.MethodGet, ts.URL+"/api/setup/status", nil, "")
	statusBody := decodeJSON(t, statusResp)
	if needsSetup, _ := statusBody["needs_setup"].(bool); needsSetup {
		t.Fatal("expected needs_setup=false after setup")
	}

	// Step A: POST /api/setup/reset WITHOUT auth → 401.
	t.Run("stepA_reset_no_auth", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/reset", map[string]string{
			"confirm": "DELETE",
		}, "")
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
		}
	})

	// Step B: POST /api/setup/reset WITH auth but wrong confirm → 400.
	t.Run("stepB_reset_wrong_confirm", func(t *testing.T) {
		for _, bad := range []string{"delete", "yes", "RESET", ""} {
			resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/reset", map[string]string{
				"confirm": bad,
			}, authToken)
			resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("confirm=%q: expected 400, got %d", bad, resp.StatusCode)
			}
		}
	})

	// Step C: POST /api/setup/reset WITH auth and confirm "DELETE" → 200, needs_setup: true.
	t.Run("stepC_reset_confirmed", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/reset", map[string]string{
			"confirm": "DELETE",
		}, authToken)
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", resp.StatusCode, body)
		}
		if success, _ := body["success"].(bool); !success {
			t.Errorf("expected success=true, got %v", body["success"])
		}
		if needsSetup, _ := body["needs_setup"].(bool); !needsSetup {
			t.Errorf("expected needs_setup=true in reset response, got %v", body["needs_setup"])
		}
	})

	// Step D: GET /api/setup/status → needs_setup: true (wizard re-appears).
	t.Run("stepD_status_after_reset", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodGet, ts.URL+"/api/setup/status", nil, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if needsSetup, _ := body["needs_setup"].(bool); !needsSetup {
			t.Errorf("expected needs_setup=true after reset, got false")
		}
	})

	// Step E: POST /api/setup/validate-key → 200 (no longer locked after reset).
	t.Run("stepE_validate_key_unlocked_after_reset", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/validate-key", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-new",
			"model":    "claude-sonnet-4-6",
		}, "")
		body := decodeJSON(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 after reset, got %d: %v", resp.StatusCode, body)
		}
		if valid, _ := body["valid"].(bool); !valid {
			t.Errorf("expected valid=true, got %v", body["valid"])
		}
	})
}

// TestIntegration_AuthBypass verifies which setup endpoints require auth and which do not.
func TestIntegration_AuthBypass(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := &config.Config{
		Agent:   config.AgentConfig{Name: "test-agent"},
		Channel: config.ChannelConfig{Type: "cli"},
		Web: config.WebConfig{
			Host:      "127.0.0.1",
			Port:      8080,
			AuthToken: "secret-tok", // auth is enforced
		},
	}

	_, ts := newSetupIntegrationServer(t, cfg, cfgPath)
	client := ts.Client()

	// These endpoints must return 200 WITHOUT an Authorization header.
	noAuthEndpoints := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/setup/status", nil},
		{http.MethodGet, "/api/setup/providers", nil},
		{http.MethodPost, "/api/setup/validate-key", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test",
			"model":    "claude-sonnet-4-6",
		}},
		{http.MethodPost, "/api/setup/complete", map[string]string{
			"provider": "anthropic",
			"api_key":  "sk-ant-test",
			"model":    "claude-sonnet-4-6",
		}},
	}

	for _, ep := range noAuthEndpoints {
		ep := ep
		t.Run("no_auth_"+ep.method+"_"+ep.path, func(t *testing.T) {
			resp := doJSON(t, client, ep.method, ts.URL+ep.path, ep.body, "")
			resp.Body.Close()

			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s %s: should bypass auth, got 401", ep.method, ep.path)
			}
		})
	}

	// POST /api/setup/reset MUST require auth.
	t.Run("reset_requires_auth", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/reset", map[string]string{
			"confirm": "DELETE",
		}, "")
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("reset without auth: expected 401, got %d", resp.StatusCode)
		}
	})

	// POST /api/setup/reset WITH valid auth must NOT return 401.
	t.Run("reset_with_auth_passes_auth_check", func(t *testing.T) {
		resp := doJSON(t, client, http.MethodPost, ts.URL+"/api/setup/reset", map[string]string{
			"confirm": "WRONG", // intentionally wrong — we only care about auth check passing
		}, "secret-tok")
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("reset with valid auth: expected not 401, got 401")
		}
	})
}
