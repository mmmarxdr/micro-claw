package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"microagent/internal/config"
	"microagent/internal/provider"
	"microagent/internal/setup"
)

// setupStatusResponse is the response body for GET /api/setup/status.
type setupStatusResponse struct {
	NeedsSetup bool     `json:"needs_setup"`
	Missing    []string `json:"missing"`
}

// handleGetSetupStatus returns whether the system needs first-time setup.
// Always returns HTTP 200 — this is a status query, not an error condition.
// This endpoint bypasses auth middleware.
func (s *Server) handleGetSetupStatus(w http.ResponseWriter, r *http.Request) {
	ok, missing := config.IsProviderConfigured(*s.deps.Config)
	resp := setupStatusResponse{
		NeedsSetup: !ok,
		Missing:    missing,
	}
	writeJSON(w, http.StatusOK, resp)
}

// modelInfoJSON is the JSON representation of a setup.ModelInfo.
type modelInfoJSON struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	CostIn      float64 `json:"cost_in"`
	CostOut     float64 `json:"cost_out"`
	ContextK    int     `json:"context_k"`
	Description string  `json:"description"`
}

// providerInfoJSON is the JSON representation of a provider with its models.
type providerInfoJSON struct {
	DisplayName    string         `json:"display_name"`
	RequiresAPIKey bool           `json:"requires_api_key"`
	SupportsBaseURL bool          `json:"supports_base_url"`
	DefaultBaseURL string         `json:"default_base_url,omitempty"`
	Models         []modelInfoJSON `json:"models"`
}

// providerMeta holds static metadata for each supported provider.
var providerMeta = map[string]struct {
	DisplayName    string
	RequiresAPIKey bool
	SupportsBaseURL bool
	DefaultBaseURL string
}{
	"anthropic":  {DisplayName: "Anthropic", RequiresAPIKey: true},
	"openai":     {DisplayName: "OpenAI", RequiresAPIKey: true},
	"gemini":     {DisplayName: "Google Gemini", RequiresAPIKey: true},
	"deepseek":   {DisplayName: "Deepseek", RequiresAPIKey: true},
	"qwen":       {DisplayName: "Qwen", RequiresAPIKey: true},
	"openrouter": {DisplayName: "OpenRouter", RequiresAPIKey: true},
	"ollama":     {DisplayName: "Ollama", SupportsBaseURL: true, DefaultBaseURL: "http://localhost:11434"},
}

// providersResponse wraps the provider catalog for JSON serialization.
type providersResponse struct {
	Providers map[string]providerInfoJSON `json:"providers"`
}

// handleGetSetupProviders returns the provider catalog as JSON.
// Each provider includes display metadata and its model list.
// Ollama is represented with an empty model array (free-text entry).
// OtherModelSentinel entries (id == "") are excluded.
// This endpoint bypasses auth middleware.
func (s *Server) handleGetSetupProviders(w http.ResponseWriter, r *http.Request) {
	result := make(map[string]providerInfoJSON)

	for prov, models := range setup.ProviderCatalog {
		meta := providerMeta[prov]
		entries := make([]modelInfoJSON, 0, len(models))
		for _, m := range models {
			if m.ID == "" {
				continue
			}
			entries = append(entries, modelInfoJSON{
				ID:          m.ID,
				DisplayName: m.DisplayName,
				CostIn:      m.CostIn,
				CostOut:     m.CostOut,
				ContextK:    m.ContextK,
				Description: m.Description,
			})
		}
		result[prov] = providerInfoJSON{
			DisplayName:    meta.DisplayName,
			RequiresAPIKey: meta.RequiresAPIKey,
			SupportsBaseURL: meta.SupportsBaseURL,
			DefaultBaseURL: meta.DefaultBaseURL,
			Models:         entries,
		}
	}

	// Ollama is absent from ProviderCatalog — add it with empty models.
	if _, ok := result["ollama"]; !ok {
		meta := providerMeta["ollama"]
		result["ollama"] = providerInfoJSON{
			DisplayName:    meta.DisplayName,
			RequiresAPIKey: false,
			SupportsBaseURL: meta.SupportsBaseURL,
			DefaultBaseURL: meta.DefaultBaseURL,
			Models:         []modelInfoJSON{},
		}
	}

	writeJSON(w, http.StatusOK, providersResponse{Providers: result})
}

// validateKeyRequest is the request body for POST /api/setup/validate-key.
type validateKeyRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

// validateKeyResponse is the response body for POST /api/setup/validate-key.
type validateKeyResponse struct {
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

// handleValidateKey validates a provider API key by making a live HealthCheck.
// Returns 403 if setup is already complete (to prevent re-validation when configured).
// Uses a 10-second timeout on the validation call.
// This endpoint bypasses auth middleware.
func (s *Server) handleValidateKey(w http.ResponseWriter, r *http.Request) {
	// Guard: if already configured, this endpoint is disabled.
	if ok, _ := config.IsProviderConfigured(*s.deps.Config); ok {
		writeError(w, http.StatusForbidden, "setup already complete")
		return
	}

	var req validateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	provCfg := config.ProviderConfig{
		Type:    req.Provider,
		Model:   req.Model,
		APIKey:  req.APIKey,
		BaseURL: req.BaseURL,
	}

	factory := s.deps.ProviderFactory
	if factory == nil {
		factory = provider.NewFromConfig
	}

	prov, err := factory(provCfg)
	if err != nil {
		writeJSON(w, http.StatusOK, validateKeyResponse{Valid: false, Error: err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if _, err := prov.HealthCheck(ctx); err != nil {
		writeJSON(w, http.StatusOK, validateKeyResponse{Valid: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, validateKeyResponse{Valid: true})
}

// setupCompleteRequest is the request body for POST /api/setup/complete.
type setupCompleteRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

// setupCompleteResponse is the response body for POST /api/setup/complete.
type setupCompleteResponse struct {
	Success         bool   `json:"success"`
	AuthToken       string `json:"auth_token"`
	ConfigPath      string `json:"config_path"`
	RestartRequired bool   `json:"restart_required"`
}

// handleSetupComplete writes the provider config atomically and reloads in-memory state.
// If a config file already exists, non-provider fields are preserved.
// Generates an auth token if one is not already set.
func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	var req setupCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields.
	if req.Provider == "" {
		writeError(w, http.StatusBadRequest, "provider is required")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if req.Provider != "ollama" && req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required for this provider")
		return
	}

	cfgPath := s.deps.ConfigPath
	if cfgPath == "" {
		// Fallback: write next to a default path.
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".microagent", "config.yaml")
	}

	// Capture the current active provider before any changes (for restart detection).
	prevType := s.deps.Config.Models.Default.Provider

	// Load existing config if present, otherwise start with defaults.
	var base config.Config
	existingData, err := os.ReadFile(cfgPath)
	if err == nil {
		if err2 := yaml.Unmarshal(existingData, &base); err2 != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to parse existing config: %v", err2))
			return
		}
		// Run migration on the loaded config so legacy v1 provider fields are absorbed.
		config.MigrateLegacyProviderPublic(&base)
	} else {
		// First-time defaults.
		base.Web.Enabled = true
		base.Store.Type = "sqlite"
		if home, err2 := os.UserHomeDir(); err2 == nil {
			base.Store.Path = filepath.Join(home, ".microagent", "data")
		}
	}

	// Restart is required when switching provider type on an already-configured system.
	restartRequired := prevType != "" && prevType != req.Provider

	// Write v2 shape: populate Providers map and Models.Default.
	if base.Providers == nil {
		base.Providers = make(map[string]config.ProviderCredentials)
	}
	creds := base.Providers[req.Provider]
	creds.APIKey = req.APIKey
	creds.BaseURL = req.BaseURL
	base.Providers[req.Provider] = creds
	base.Models.Default.Provider = req.Provider
	base.Models.Default.Model = req.Model
	// Nil out legacy Provider pointer so it won't be serialized.
	base.Provider = nil

	// Ensure auth token exists. Prefer the current in-memory token (set at
	// startup), then fall back to the on-disk value, then generate a new one.
	authToken := s.deps.Config.Web.AuthToken
	if authToken == "" {
		authToken = base.Web.AuthToken
	}
	if authToken == "" {
		t, err := GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate auth token")
			return
		}
		authToken = t
	}
	base.Web.AuthToken = authToken

	// Atomic write: temp file + rename.
	if err := config.AtomicWriteConfig(cfgPath, &base); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to write config: %v", err))
		return
	}

	// Reload provider fields in-memory (v2 shape).
	s.deps.Config.Providers = base.Providers
	s.deps.Config.Models = base.Models
	s.deps.Config.Provider = nil // clear legacy pointer
	s.deps.Config.Web.AuthToken = authToken

	// Set HttpOnly cookie so the browser is authenticated automatically.
	setAuthCookie(w, r, authToken)

	writeJSON(w, http.StatusOK, setupCompleteResponse{
		Success:         true,
		AuthToken:       authToken,
		ConfigPath:      cfgPath,
		RestartRequired: restartRequired,
	})
}

// handleSetupReset clears provider fields from config and disk.
// Requires Bearer auth. Request body must contain {"confirm":"DELETE"}.
func (s *Server) handleSetupReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Confirm != "DELETE" {
		writeError(w, http.StatusBadRequest, `confirm must be exactly "DELETE"`)
		return
	}

	cfgPath := s.deps.ConfigPath
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".microagent", "config.yaml")
	}

	// Load existing config to preserve non-provider fields.
	var base config.Config
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err2 := yaml.Unmarshal(data, &base); err2 != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to parse config: %v", err2))
			return
		}
	}

	// Clear v2 provider fields.
	base.Providers = nil
	base.Models.Default = config.ModelRef{}
	base.Provider = nil // nil out any legacy pointer too

	if err := config.AtomicWriteConfig(cfgPath, &base); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to write config: %v", err))
		return
	}

	// Reload in-memory.
	s.deps.Config.Providers = nil
	s.deps.Config.Models.Default = config.ModelRef{}
	s.deps.Config.Provider = nil

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "needs_setup": true})
}

