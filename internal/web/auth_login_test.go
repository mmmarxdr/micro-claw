package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"microagent/internal/config"
)

// newLoginTestServer creates a Server with a preconfigured auth token.
func newLoginTestServer(t *testing.T, token string) *Server {
	t.Helper()
	cfg := minimalConfig()
	cfg.Web.AuthToken = token
	cfg.Web.AuthTokenIssuedAt = time.Now()
	return &Server{deps: ServerDeps{Config: cfg}}
}

func loginBody(t *testing.T, token string) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	return bytes.NewBuffer(b)
}

// TestLogin_HappyPath verifies FR-10..FR-12, FR-56, AS-2:
// valid token → 204, Set-Cookie header present, no response body token.
func TestLogin_HappyPath(t *testing.T) {
	const tok = "correct-token-for-login-test"
	s := newLoginTestServer(t, tok)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody(t, tok))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("happy path: expected 204, got %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "auth" {
			authCookie = c
			break
		}
	}
	if authCookie == nil {
		t.Fatal("happy path: no auth cookie set")
	}
	if authCookie.Value == "" {
		t.Error("happy path: auth cookie has empty value")
	}
	if !authCookie.HttpOnly {
		t.Error("happy path: auth cookie must be HttpOnly")
	}
	if authCookie.MaxAge != authCookieMaxAge {
		t.Errorf("happy path: MaxAge got %d, want %d", authCookie.MaxAge, authCookieMaxAge)
	}
	// FR-14: no token in response body.
	body := w.Body.String()
	if body != "" {
		t.Errorf("happy path: response body should be empty, got %q", body)
	}
}

// TestLogin_WrongToken verifies FR-13, AS-19:
// wrong token → 401, no Set-Cookie header.
func TestLogin_WrongToken(t *testing.T) {
	const tok = "correct-token-for-login-test"
	s := newLoginTestServer(t, tok)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody(t, "wrong-token"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: expected 401, got %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "auth" {
			t.Error("wrong token: auth cookie must not be set on 401")
		}
	}
}

// TestLogin_PreSetupPassthrough verifies FR-15, FR-23:
// when AuthToken is empty (pre-setup), login must allow the request through.
// The middleware will handle the pre-setup bypass; but login itself is outside
// the middleware. We test that login with an empty server token responds with
// a special case: the spec says the endpoint is exempt from auth middleware
// (FR-15) — the handler itself is what gets called. When no token is set,
// the server is in pre-setup mode and login is meaningless; we expect 401
// with no cookie (cannot validate nothing).
func TestLogin_PreSetupMode_Returns401(t *testing.T) {
	s := newLoginTestServer(t, "") // empty token = pre-setup mode
	s.deps.Config.Web.AuthToken = ""

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody(t, "any-token"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("pre-setup: expected 401, got %d", resp.StatusCode)
	}
}

// TestLogin_DoesNotModifyIssuedAt verifies FR-56, AS-23:
// a successful login must NOT update or reset AuthTokenIssuedAt.
func TestLogin_DoesNotModifyIssuedAt(t *testing.T) {
	const tok = "correct-token-for-login-test"
	s := newLoginTestServer(t, tok)

	// Artificially set IssuedAt to 10 days ago.
	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	s.deps.Config.Web.AuthTokenIssuedAt = tenDaysAgo

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody(t, tok))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if !s.deps.Config.Web.AuthTokenIssuedAt.Equal(tenDaysAgo) {
		t.Errorf("IssuedAt was modified by login: got %v, want %v",
			s.deps.Config.Web.AuthTokenIssuedAt, tenDaysAgo)
	}
}

// TestLogin_BadContentType verifies that a missing body returns 400.
func TestLogin_MalformedBody(t *testing.T) {
	const tok = "correct-token-for-login-test"
	s := newLoginTestServer(t, tok)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body: expected 400, got %d", resp.StatusCode)
	}
}

// TestLogin_SameSite verifies FR-5 / FR-6:
// SameSite=Strict when AllowedOrigins empty, SameSite=None when non-empty.
func TestLogin_SameSite_SameOrigin(t *testing.T) {
	const tok = "samesite-test-token"
	cfg := &config.Config{
		Web: config.WebConfig{
			AuthToken:    tok,
			AllowedOrigins: nil, // same-origin
		},
	}
	s := &Server{deps: ServerDeps{Config: cfg}}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody(t, tok))
	w := httptest.NewRecorder()

	s.handleLogin(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "auth" {
			if c.SameSite != http.SameSiteStrictMode {
				t.Errorf("same-origin: SameSite got %v, want Strict", c.SameSite)
			}
			return
		}
	}
	t.Fatal("auth cookie not found")
}
