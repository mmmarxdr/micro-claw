package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"daimon/internal/config"
)

// patchBody is the narrow request shape accepted by PUT /api/config.
// We decode only the fields the UI can edit. Every other top-level field
// (web, channel, agent, store, audit, etc.) is preserved from the stored
// config as-is — decoding into a full config.Config{} would zero out absent
// fields, destroying unrelated configuration on every PUT.
//
// Tools uses a narrow patch shape so the UI-editable subtrees (shell, file,
// http) can be replaced without touching tools.web_fetch or tools.mcp, which
// the UI does not expose.
type patchBody struct {
	Providers map[string]config.ProviderCredentials `json:"providers,omitempty"`
	Models    *config.ModelsConfig                  `json:"models,omitempty"`
	Tools     *patchTools                           `json:"tools,omitempty"`
	RAG       *patchRAG                             `json:"rag,omitempty"`
}

// patchTools mirrors config.ToolsConfig but with pointer fields so absent
// keys are distinguishable from zero-valued ones. Only the UI-exposed
// subtrees are accepted; web_fetch and mcp are deliberately omitted and
// preserved from the stored config.
type patchTools struct {
	Shell *config.ShellToolConfig `json:"shell,omitempty"`
	File  *config.FileToolConfig  `json:"file,omitempty"`
	HTTP  *config.HTTPToolConfig  `json:"http,omitempty"`
}

// patchRAG accepts the UI-editable subtree of RAGConfig. When a new field is
// added to the Settings UI, it MUST also be allow-listed here — otherwise the
// JSON decoder silently drops it on PUT and the toast lies about success.
type patchRAG struct {
	Embedding *config.RAGEmbeddingConf `json:"embedding,omitempty"`
}

// maxPutBodySize is the hard limit for PUT /api/config request bodies (64 KB).
const maxPutBodySize = 64 * 1024

// handleGetConfig returns the current config with all sensitive fields masked.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := *s.deps.Config // shallow copy

	// Mask all provider api_keys in the v2 Providers map.
	if cfg.Providers != nil {
		masked := make(map[string]config.ProviderCredentials, len(cfg.Providers))
		for name, creds := range cfg.Providers {
			creds.APIKey = config.MaskSecret(creds.APIKey)
			masked[name] = creds
		}
		cfg.Providers = masked
	}

	// Also mask the legacy Provider pointer api_key if present (defensive).
	if cfg.Provider != nil {
		p := *cfg.Provider
		p.APIKey = config.MaskSecret(p.APIKey)
		cfg.Provider = &p
	}

	// Mask channel tokens and web auth token.
	cfg.Channel.Token = config.MaskSecret(cfg.Channel.Token)
	cfg.Channel.AccessToken = config.MaskSecret(cfg.Channel.AccessToken)
	cfg.Channel.VerifyToken = config.MaskSecret(cfg.Channel.VerifyToken)
	cfg.Web.AuthToken = config.MaskSecret(cfg.Web.AuthToken)

	// Mask fallback api_key (now on Config directly, per OQ-4).
	if cfg.Fallback != nil {
		fb := *cfg.Fallback
		fb.APIKey = config.MaskSecret(fb.APIKey)
		cfg.Fallback = &fb
	}

	writeJSON(w, http.StatusOK, cfg)
}

// handlePutConfig applies a partial update to the running config.
// Auth is enforced by the auth middleware; no per-handler token check needed.
//
// Algorithm (design §4):
//  1. Decode body (≤64KB) into patchBody (providers + models only).
//  2. Validate provider names.
//  3. Strip masked api_key values (server-side defensive strip).
//  4. Acquire configMu; deep-copy Providers map.
//  5. Merge body.Providers (field-level: empty = preserve).
//  6. Merge body.Models (non-empty fields only).
//  7. validateActiveCredentials.
//  8. atomicWriteConfig.
//  9. Swap in-memory config pointer.
// 10. Return masked GET body.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	// Limit body size before decoding.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPutBodySize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if int64(len(body)) > maxPutBodySize {
		writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 64KB limit")
		return
	}

	var patch patchBody
	if err := json.Unmarshal(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	// Validate provider names before acquiring the lock (cheap, read-only).
	if patch.Providers != nil {
		if err := validateProviderNames(patch.Providers); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Strip masked api_key values (defensive, server-side).
		stripMaskedAPIKeys(patch.Providers)
	}

	cfgPath := s.deps.ConfigPath

	// Guard the read-copy-validate-write sequence.
	s.configMu.Lock()
	defer s.configMu.Unlock()

	// Deep-copy current config — never mutate the shared struct.
	merged := *s.deps.Config
	merged.Providers = deepCopyProviders(s.deps.Config.Providers)

	// Merge body.Providers (field-level: empty string = preserve existing).
	if patch.Providers != nil {
		if merged.Providers == nil {
			merged.Providers = make(map[string]config.ProviderCredentials)
		}
		for name, creds := range patch.Providers {
			existing := merged.Providers[name]
			if creds.APIKey != "" {
				existing.APIKey = creds.APIKey
			}
			if creds.BaseURL != "" {
				existing.BaseURL = creds.BaseURL
			}
			merged.Providers[name] = existing
		}
	}

	// Merge body.Models (non-empty fields only).
	if patch.Models != nil {
		if patch.Models.Default.Provider != "" {
			merged.Models.Default.Provider = patch.Models.Default.Provider
		}
		if patch.Models.Default.Model != "" {
			merged.Models.Default.Model = patch.Models.Default.Model
		}
	}

	// Merge body.Tools (subtree-level: a present subtree fully replaces the
	// stored one; absent subtrees — and web_fetch / mcp — are preserved).
	if patch.Tools != nil {
		if patch.Tools.Shell != nil {
			merged.Tools.Shell = *patch.Tools.Shell
		}
		if patch.Tools.File != nil {
			merged.Tools.File = *patch.Tools.File
		}
		if patch.Tools.HTTP != nil {
			merged.Tools.HTTP = *patch.Tools.HTTP
		}
	}

	// Merge body.RAG. Embedding subtree replaces wholesale when present;
	// missing fields (e.g. user didn't fill in api_key) are preserved from the
	// stored config so re-saving from a partially-loaded form doesn't blank out
	// secrets the UI never sees.
	if patch.RAG != nil && patch.RAG.Embedding != nil {
		newEmb := *patch.RAG.Embedding
		stored := merged.RAG.Embedding
		if newEmb.APIKey == "" {
			newEmb.APIKey = stored.APIKey
		}
		if newEmb.BaseURL == "" {
			newEmb.BaseURL = stored.BaseURL
		}
		merged.RAG.Embedding = newEmb
	}

	// Validate active provider has credentials.
	if err := validateActiveCredentials(merged); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Write to disk atomically (config path may be empty in tests — skip if so).
	if cfgPath != "" {
		if err := config.AtomicWriteConfig(cfgPath, &merged); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to write config: %v", err))
			return
		}
	}

	// Swap in-memory config.
	*s.deps.Config = merged

	// Respond with the masked GET body (same shape as GET /api/config).
	// Re-use the same masking logic to avoid drift.
	s.handleGetConfig(w, r)
}

// stripMaskedAPIKeys clears any APIKey in providers whose value matches the
// masked sentinel pattern. Mutates the map in place.
// This is the server-side defensive strip (FR-19); the client also strips proactively.
func stripMaskedAPIKeys(providers map[string]config.ProviderCredentials) {
	for name, creds := range providers {
		if config.IsMasked(creds.APIKey) {
			creds.APIKey = ""
			providers[name] = creds
		}
	}
}

// validateProviderNames returns an error if any key in providers is not a
// known provider name. The error message lists the valid names (FR-20).
func validateProviderNames(providers map[string]config.ProviderCredentials) error {
	for name := range providers {
		if !config.IsKnownProvider(name) {
			return fmt.Errorf(
				"unknown provider %q: valid providers are %s",
				name,
				strings.Join(config.KnownProviders, ", "),
			)
		}
	}
	return nil
}

// validateActiveCredentials returns an error when the active provider
// (cfg.Models.Default.Provider) has no api_key and is not "ollama" (FR-21).
func validateActiveCredentials(cfg config.Config) error {
	prov := cfg.Models.Default.Provider
	if prov == "" {
		// No active provider — nothing to validate; setup-only mode.
		return nil
	}
	if prov == "ollama" {
		// Ollama does not require an API key.
		return nil
	}
	creds, ok := cfg.Providers[prov]
	if !ok || creds.APIKey == "" {
		return fmt.Errorf("active provider has no credentials")
	}
	return nil
}

// deepCopyProviders returns a new map with the same entries as src.
// Callers must not share the returned map with src.
func deepCopyProviders(src map[string]config.ProviderCredentials) map[string]config.ProviderCredentials {
	if src == nil {
		return nil
	}
	dst := make(map[string]config.ProviderCredentials, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
