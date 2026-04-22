package web

// T14 – PUT /api/config accepts rag.hyde sub-tree; values persist.
// T15 – PUT /api/config preserves unspecified hyde fields (regression guard).
// T16 – PUT /api/config with missing hyde key leaves stored hyde intact.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"daimon/internal/config"
)

// ---------------------------------------------------------------------------
// T14 – PUT accepts rag.hyde sub-tree; values survive round-trip
// ---------------------------------------------------------------------------

func TestHandlePutConfig_RAGHyde_AcceptsSubtree(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	body := []byte(`{"rag":{"hyde":{"enabled":true,"query_weight":0.5}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T14: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify in-memory config updated.
	got := s.deps.Config.RAG.Hyde
	if !got.Enabled {
		t.Error("T14: Hyde.Enabled: want true, got false")
	}
	if got.QueryWeight != 0.5 {
		t.Errorf("T14: Hyde.QueryWeight: want 0.5, got %v", got.QueryWeight)
	}

	// Verify response body also reflects the values.
	var respCfg config.Config
	if err := json.NewDecoder(rec.Body).Decode(&respCfg); err != nil {
		t.Fatalf("T14: decode response: %v", err)
	}
	if !respCfg.RAG.Hyde.Enabled {
		t.Error("T14: response Hyde.Enabled: want true, got false")
	}
	if respCfg.RAG.Hyde.QueryWeight != 0.5 {
		t.Errorf("T14: response Hyde.QueryWeight: want 0.5, got %v", respCfg.RAG.Hyde.QueryWeight)
	}
}

// ---------------------------------------------------------------------------
// T15 – REGRESSION GUARD: unspecified hyde fields are preserved (mirrors T16
// from rag-retrieval-precision). Sending a partial hyde patch MUST NOT reset
// the unmentioned fields to zero.
// ---------------------------------------------------------------------------

func TestHandlePutConfig_RAGHyde_PreservesUnspecifiedFields(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	// Seed a fully-populated hyde config.
	cfg.RAG.Hyde = config.RAGHydeConf{
		Enabled:     true,
		Model:       "gemini-2.5-flash",
		QueryWeight: 0.5,
		MaxCandidates: 30,
	}

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	// Only PUT max_candidates — other fields must survive.
	body := []byte(`{"rag":{"hyde":{"max_candidates":50}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T15: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	got := s.deps.Config.RAG.Hyde
	// Enabled and Model were NOT in the patch — must be preserved from seed.
	if !got.Enabled {
		t.Error("T15: Hyde.Enabled should be preserved as true")
	}
	if got.Model != "gemini-2.5-flash" {
		t.Errorf("T15: Hyde.Model should be preserved as gemini-2.5-flash, got %q", got.Model)
	}
	// QueryWeight was not in the patch — preserved.
	if got.QueryWeight != 0.5 {
		t.Errorf("T15: Hyde.QueryWeight should be preserved as 0.5, got %v", got.QueryWeight)
	}
	// MaxCandidates was in the patch — updated.
	if got.MaxCandidates != 50 {
		t.Errorf("T15: Hyde.MaxCandidates should be 50, got %d", got.MaxCandidates)
	}
}

// ---------------------------------------------------------------------------
// T16 – PUT /api/config with no hyde key leaves stored hyde intact
// ---------------------------------------------------------------------------

func TestHandlePutConfig_RAGHyde_AbsentKeyPreservesHyde(t *testing.T) {
	cfg := minimalConfig()
	cfg.Web.AuthToken = "secret-token"
	// Seed a non-default hyde config.
	cfg.RAG.Hyde = config.RAGHydeConf{
		Enabled:     true,
		Model:       "some-model",
		QueryWeight: 0.4,
	}

	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"

	s := newConfigTestServer(cfg, cfgPath)

	// PUT only the embedding subtree — hyde must be untouched.
	body := []byte(`{"rag":{"embedding":{"enabled":false}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader("secret-token"))
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("T16: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	got := s.deps.Config.RAG.Hyde
	if !got.Enabled {
		t.Error("T16: Hyde.Enabled should be preserved as true")
	}
	if got.Model != "some-model" {
		t.Errorf("T16: Hyde.Model should be preserved, got %q", got.Model)
	}
	if got.QueryWeight != 0.4 {
		t.Errorf("T16: Hyde.QueryWeight should be preserved as 0.4, got %v", got.QueryWeight)
	}
}
