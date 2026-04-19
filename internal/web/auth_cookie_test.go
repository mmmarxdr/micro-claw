package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"daimon/internal/config"
)

// makeWebCfg returns a minimal WebConfig for testing cookie helpers.
func makeWebCfg(trustProxy bool, origins ...string) *config.WebConfig {
	return &config.WebConfig{
		TrustProxy:     trustProxy,
		AllowedOrigins: origins,
	}
}

// parseCookies parses the Set-Cookie headers from a ResponseRecorder.
func parseCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	resp := &http.Response{Header: rec.Header()}
	return resp.Cookies()
}

// findCookie returns the named cookie from the recorder, or nil.
func findCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range parseCookies(rec) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestSetAuthCookie_BasicFlags verifies FR-4: HttpOnly, Path=/, Max-Age=2592000.
func TestSetAuthCookie_BasicFlags(t *testing.T) {
	cfg := makeWebCfg(false)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	setAuthCookie(rec, req, cfg, "my-token")

	c := findCookie(rec, "auth")
	if c == nil {
		t.Fatal("auth cookie not set")
	}
	if c.Value != "my-token" {
		t.Errorf("cookie value: got %q, want %q", c.Value, "my-token")
	}
	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if c.Path != "/" {
		t.Errorf("cookie path: got %q, want %q", c.Path, "/")
	}
	if c.MaxAge != authCookieMaxAge {
		t.Errorf("cookie Max-Age: got %d, want %d", c.MaxAge, authCookieMaxAge)
	}
}

// TestSetAuthCookie_SameSiteStrict verifies FR-5/AS-6: SameSite=Strict when AllowedOrigins empty.
func TestSetAuthCookie_SameSiteStrict(t *testing.T) {
	cfg := makeWebCfg(false) // no AllowedOrigins → same-origin mode
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	setAuthCookie(rec, req, cfg, "tok")

	c := findCookie(rec, "auth")
	if c == nil {
		t.Fatal("auth cookie not set")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite: got %v, want Strict", c.SameSite)
	}
}

// TestSetAuthCookie_SameSiteNone verifies FR-6/AS-5: SameSite=None when AllowedOrigins non-empty.
func TestSetAuthCookie_SameSiteNone(t *testing.T) {
	cfg := makeWebCfg(false, "https://app.example.com") // cross-origin mode
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	setAuthCookie(rec, req, cfg, "tok")

	c := findCookie(rec, "auth")
	if c == nil {
		t.Fatal("auth cookie not set")
	}
	if c.SameSite != http.SameSiteNoneMode {
		t.Errorf("SameSite: got %v, want None", c.SameSite)
	}
	if !c.Secure {
		t.Error("SameSite=None requires Secure flag")
	}
}

// TestResolveSecureFlag_TLS verifies FR-7: Secure set when r.TLS != nil.
func TestResolveSecureFlag_TLS(t *testing.T) {
	cfg := makeWebCfg(false)
	req := httptest.NewRequest("GET", "https://localhost/", nil)
	req.TLS = &tls.ConnectionState{} // non-nil → TLS active
	result := resolveSecureFlag(req, cfg)
	if !result {
		t.Error("resolveSecureFlag: expected true when r.TLS != nil")
	}
}

// TestResolveSecureFlag_TrustProxyWithForwardedHTTPS verifies FR-7/AS-7.
func TestResolveSecureFlag_TrustProxyWithForwardedHTTPS(t *testing.T) {
	cfg := makeWebCfg(true) // TrustProxy=true
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")

	if !resolveSecureFlag(req, cfg) {
		t.Error("resolveSecureFlag: expected true when TrustProxy=true and X-Forwarded-Proto: https")
	}
}

// TestResolveSecureFlag_NoTrustProxySpoofed verifies FR-8/AS-8: spoof protection.
func TestResolveSecureFlag_NoTrustProxySpoofed(t *testing.T) {
	cfg := makeWebCfg(false) // TrustProxy=false
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.Header.Set("X-Forwarded-Proto", "https") // spoofed — must be ignored

	if resolveSecureFlag(req, cfg) {
		t.Error("resolveSecureFlag: must NOT set Secure when TrustProxy=false, even if X-Forwarded-Proto: https")
	}
}

// TestClearAuthCookie_MirrorsSetAuthCookie verifies that clearAuthCookie uses
// MaxAge=0 (not -1) and mirrors the same SameSite/Secure resolution as setAuthCookie,
// so browsers accept the clearing cookie (FR-18).
func TestClearAuthCookie_MirrorsSetAuthCookieAttributes(t *testing.T) {
	cfg := makeWebCfg(true, "https://app.example.com")
	req := httptest.NewRequest("GET", "http://localhost/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	clearAuthCookie(rec, req, cfg)

	c := findCookie(rec, "auth")
	if c == nil {
		t.Fatal("clearAuthCookie: no auth cookie in response")
	}
	if c.MaxAge != 0 {
		t.Errorf("clearAuthCookie: MaxAge should be 0, got %d", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("clearAuthCookie: value should be empty, got %q", c.Value)
	}
	if !c.HttpOnly {
		t.Error("clearAuthCookie: must be HttpOnly")
	}
	// Cross-origin config → SameSite=None + Secure.
	if c.SameSite != http.SameSiteNoneMode {
		t.Errorf("clearAuthCookie: SameSite mismatch: got %v, want None", c.SameSite)
	}
	if !c.Secure {
		t.Error("clearAuthCookie: Secure flag mismatch (cross-origin mode requires Secure)")
	}
}

// TestAuthCookieMaxAge verifies FR-1: the constant is 30*24*60*60 = 2592000.
func TestAuthCookieMaxAge(t *testing.T) {
	const want = 30 * 24 * 60 * 60
	if authCookieMaxAge != want {
		t.Errorf("authCookieMaxAge: got %d, want %d", authCookieMaxAge, want)
	}
}
