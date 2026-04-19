package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// nopHandlerSimple is a trivial handler for CORS middleware tests.
var nopHandlerSimple = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// makeCORSRequest sends a request through corsMiddleware and returns the response.
func makeCORSRequest(t *testing.T, allowedOrigins []string, origin string) *http.Response {
	t.Helper()
	handler := corsMiddleware(allowedOrigins, nopHandlerSimple)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Result()
}

// TestCORS_EmptyOrigins_NoCredentials verifies FR-31, AS-20, INV-5:
// when AllowedOrigins is empty, Access-Control-Allow-Credentials MUST NOT be set.
func TestCORS_EmptyOrigins_NoCredentials(t *testing.T) {
	resp := makeCORSRequest(t, nil, "https://any.example.com")
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("empty AllowedOrigins: expected no Allow-Credentials header, got %q", got)
	}
}

// TestCORS_ExplicitOrigins_AllowsCredentials verifies FR-30:
// when AllowedOrigins is set and origin matches, Allow-Credentials: true is set.
func TestCORS_ExplicitOrigins_AllowsCredentials(t *testing.T) {
	allowed := []string{"https://app.example.com"}
	resp := makeCORSRequest(t, allowed, "https://app.example.com")

	cred := resp.Header.Get("Access-Control-Allow-Credentials")
	if cred != "true" {
		t.Errorf("explicit allowed origin: expected Allow-Credentials: true, got %q", cred)
	}
	origin := resp.Header.Get("Access-Control-Allow-Origin")
	if origin != "https://app.example.com" {
		t.Errorf("explicit allowed origin: expected echoed origin, got %q", origin)
	}
}

// TestCORS_NoWildcardWithCredentials verifies FR-32, INV-5:
// wildcard + credentials is absolutely prohibited.
// Even if someone configures AllowedOrigins: ["*"], credentials header must not
// be emitted with a wildcard ACAO value.
func TestCORS_NoWildcardWithCredentials(t *testing.T) {
	// Test 1: empty AllowedOrigins sends no ACAO at all to cross-origin requests.
	resp := makeCORSRequest(t, nil, "https://any.example.com")
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	cred := resp.Header.Get("Access-Control-Allow-Credentials")
	if acao == "*" && cred == "true" {
		t.Error("wildcard ACAO with Allow-Credentials is prohibited (INV-5)")
	}

	// Test 2: unlisted origin must not receive credentials header.
	resp2 := makeCORSRequest(t, []string{"https://app.example.com"}, "https://other.example.com")
	cred2 := resp2.Header.Get("Access-Control-Allow-Credentials")
	if cred2 == "true" {
		t.Errorf("unlisted origin must not receive Allow-Credentials, got %q", cred2)
	}
}

// TestCORS_EmptyOrigins_NoOriginHeader_Passthrough verifies baseline:
// when no Origin header is present, response is a simple pass-through.
func TestCORS_EmptyOrigins_NoOriginHeader_Passthrough(t *testing.T) {
	resp := makeCORSRequest(t, nil, "") // no Origin
	if resp.StatusCode != http.StatusOK {
		t.Errorf("no-origin request: expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("no-origin: credentials header should be absent, got %q", got)
	}
}

// TestCORS_Preflight_EmptyOrigins_NoCredentials verifies FR-31 on OPTIONS:
// preflight with no AllowedOrigins must not emit Allow-Credentials.
func TestCORS_Preflight_EmptyOrigins_NoCredentials(t *testing.T) {
	handler := corsMiddleware(nil, nopHandlerSimple)
	req := httptest.NewRequest(http.MethodOptions, "/api/status", nil)
	req.Header.Set("Origin", "https://any.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("preflight empty origins: expected no Allow-Credentials, got %q", got)
	}
}
