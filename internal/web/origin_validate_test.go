package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// --------------------------------------------------------------------------
// validateOrigin unit tests (FR-26..FR-29, AS-9)
// --------------------------------------------------------------------------

// TestValidateOrigin_AllowedOrigins_Match verifies FR-26/FR-28: allowed origin passes.
func TestValidateOrigin_AllowedOrigins_Match(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://app.example.com")

	if !validateOrigin(req, allowed) {
		t.Error("allowed origin should pass validation")
	}
}

// TestValidateOrigin_AllowedOrigins_NoMatch verifies FR-28, AS-9:
// unlisted origin → rejected.
func TestValidateOrigin_AllowedOrigins_NoMatch(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	if validateOrigin(req, allowed) {
		t.Error("unlisted origin should fail validation")
	}
}

// TestValidateOrigin_EmptyAllowedOrigins_Skips verifies FR-29:
// when AllowedOrigins is empty, origin validation is skipped (returns true).
func TestValidateOrigin_EmptyAllowedOrigins_Skips(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://whatever.example.com")

	if !validateOrigin(req, nil) {
		t.Error("empty AllowedOrigins: validation should be skipped (always passes)")
	}
}

// TestValidateOrigin_RefererFallback verifies FR-27:
// when Origin is absent, Referer is used as fallback in cross-origin mode.
func TestValidateOrigin_RefererFallback_Allowed(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	// No Origin header; Referer provided.
	req.Header.Set("Referer", "https://app.example.com/dashboard")

	if !validateOrigin(req, allowed) {
		t.Error("allowed Referer should pass when Origin absent")
	}
}

// TestValidateOrigin_RefererFallback_Rejected verifies FR-28 via Referer path:
// bad Referer in cross-origin mode → rejected.
func TestValidateOrigin_RefererFallback_Rejected(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Referer", "https://evil.example.com/path")

	if validateOrigin(req, allowed) {
		t.Error("bad Referer should fail validation in cross-origin mode")
	}
}

// TestValidateOrigin_NoOriginNoReferer_CrossOriginMode verifies FR-28:
// in cross-origin mode with both Origin and Referer absent, reject.
func TestValidateOrigin_NoOriginNoReferer_CrossOriginMode(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	// No Origin, no Referer.

	if validateOrigin(req, allowed) {
		t.Error("no origin and no referer in cross-origin mode should fail")
	}
}

// --------------------------------------------------------------------------
// requireOriginIfCrossOrigin middleware tests (FR-26, FR-28, AS-9)
// --------------------------------------------------------------------------

// nopHandler is a handler that records whether it was called.
func nopHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequireOriginIfCrossOrigin_SameOriginMode_NoCheck verifies FR-29:
// when AllowedOrigins is empty, the middleware does NOT block any request.
func TestRequireOriginIfCrossOrigin_SameOriginMode_NoCheck(t *testing.T) {
	called := false
	handler := requireOriginIfCrossOrigin(nil, nopHandler(&called))

	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !called {
		t.Error("same-origin mode: handler should be called regardless of Origin")
	}
	if w.Code != http.StatusOK {
		t.Errorf("same-origin mode: expected 200, got %d", w.Code)
	}
}

// TestRequireOriginIfCrossOrigin_CrossOriginMode_BadOrigin_403 verifies FR-28, AS-9:
// unlisted origin in cross-origin mode → 403 before handler.
func TestRequireOriginIfCrossOrigin_CrossOriginMode_BadOrigin_403(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	called := false
	handler := requireOriginIfCrossOrigin(allowed, nopHandler(&called))

	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if called {
		t.Error("bad origin: handler must NOT be called")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("bad origin: expected 403, got %d", w.Code)
	}
}

// TestRequireOriginIfCrossOrigin_CrossOriginMode_GoodOrigin_Passes verifies FR-26.
func TestRequireOriginIfCrossOrigin_CrossOriginMode_GoodOrigin_Passes(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	called := false
	handler := requireOriginIfCrossOrigin(allowed, nopHandler(&called))

	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if !called {
		t.Error("good origin: handler should be called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("good origin: expected 200, got %d", w.Code)
	}
}
