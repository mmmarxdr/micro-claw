package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"microagent/internal/config"
)

// --------------------------------------------------------------------------
// WS auth tests (T-019) — FR-33..FR-36, AS-10, AS-11, AS-12
// --------------------------------------------------------------------------

// wsAuthTestServer creates a full server with a registered WS handler that
// echos 200 so we can verify the middleware allows the request through.
// We don't actually perform the WS upgrade — we just verify the middleware
// acts correctly before it would reach the upgrader.
func wsAuthTestServer(t *testing.T, token string, allowedOrigins []string) (*Server, *httptest.Server) {
	t.Helper()
	cfg := &config.Config{
		Agent:   config.AgentConfig{Name: "test-agent"},
		Channel: config.ChannelConfig{Type: "cli"},
		Web: config.WebConfig{
			Host:              "127.0.0.1",
			Port:              8080,
			AuthToken:         token,
			AuthTokenIssuedAt: time.Now(),
			AllowedOrigins:    allowedOrigins,
		},
		Providers: map[string]config.ProviderCredentials{
			"anthropic": {APIKey: "sk-ant-test"},
		},
		Models: config.ModelsConfig{Default: config.ModelRef{Provider: "anthropic", Model: "claude-test"}},
	}
	s := NewServer(ServerDeps{Config: cfg})
	ts := httptest.NewServer(s.srv.Handler)
	t.Cleanup(ts.Close)
	return s, ts
}

// TestWS_NoCookie_401 verifies AS-11, FR-33:
// a WS upgrade request with no credentials returns 401 before Upgrade.
func TestWS_NoCookie_401(t *testing.T) {
	const tok = "ws-no-cookie-token"
	_, ts := wsAuthTestServer(t, tok, nil)
	client := ts.Client()

	// /ws/metrics is always registered — use it as the WS target.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ws/metrics", nil)
	// No cookie, no bearer, no ?token=
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credentials: expected 401, got %d", resp.StatusCode)
	}
}

// TestWS_CookieOnly_Succeeds verifies AS-10, FR-34:
// a WS upgrade request with only a valid auth cookie passes the middleware.
// We use /ws/metrics and a plain HTTP client (no WS upgrade) — we just want
// to see the middleware allows the request (the WS upgrader will fail on
// non-upgrade request, which is a different error).
func TestWS_CookieOnly_Succeeds(t *testing.T) {
	const tok = "ws-cookie-only-token"
	_, ts := wsAuthTestServer(t, tok, nil)
	client := ts.Client()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ws/metrics", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: tok})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// 401 means auth failed. Any other code means the middleware passed it through.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("valid cookie: middleware should pass, got 401")
	}
}

// TestWS_LegacyTokenLosesToCookie verifies AS-12, FR-22, INV-7:
// when both a valid cookie AND a stale ?token= are present, cookie wins
// and the request succeeds (the stale ?token= is never validated).
func TestWS_LegacyTokenLosesToCookie(t *testing.T) {
	const tok = "ws-legacy-priority-token"
	_, ts := wsAuthTestServer(t, tok, nil)
	client := ts.Client()

	// Cookie has the valid token; ?token= has a stale/wrong value.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ws/metrics", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: tok})
	q := req.URL.Query()
	q.Set("token", "stale-wrong-token")
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Cookie takes priority → auth passes → not 401.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("cookie should win over stale ?token=: got 401 (cookie priority broken)")
	}
}

// TestWS_CheckOrigin_RejectsUnlisted verifies FR-35, INV-5:
// when AllowedOrigins is configured, the WS upgrader rejects connections
// from origins not in the list. We verify this at the upgrader level by
// inspecting CheckOrigin directly on the built upgrader.
func TestWS_CheckOrigin_RejectsUnlisted(t *testing.T) {
	allowedOrigins := []string{"https://app.example.com"}
	upgrader := newWSUpgrader(allowedOrigins)

	// Build a mock request from an unlisted origin.
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	if upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin: unlisted origin should be rejected")
	}
}

// TestWS_CheckOrigin_AllowsListed verifies FR-35:
// listed origin passes CheckOrigin.
func TestWS_CheckOrigin_AllowsListed(t *testing.T) {
	allowedOrigins := []string{"https://app.example.com"}
	upgrader := newWSUpgrader(allowedOrigins)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics", nil)
	req.Header.Set("Origin", "https://app.example.com")

	if !upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin: listed origin should be allowed")
	}
}

// TestWS_CheckOrigin_EmptyAllowedOrigins_CurrentBehavior documents FR-35 Phase 2 state:
// when AllowedOrigins is empty, the upgrader currently allows all origins.
// Phase 5 (T-035/T-036) will harden this to same-origin only.
// The SameSite=Strict cookie + auth middleware provide the real protection for same-origin deploys.
func TestWS_CheckOrigin_EmptyAllowedOrigins_CurrentBehavior(t *testing.T) {
	upgrader := newWSUpgrader(nil)

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics", nil)
	req.Header.Set("Origin", "https://any.example.com")

	// Phase 2: allow-all is the current behavior. Phase 5 will tighten this.
	result := upgrader.CheckOrigin(req)
	_ = result // behavior is documented here; Phase 5 will assert false
}

// TestWS_CheckOrigin_SameOrigin_NoOriginHeader verifies FR-35:
// same-origin requests with no Origin header pass CheckOrigin.
func TestWS_CheckOrigin_SameOrigin_NoOriginHeader(t *testing.T) {
	upgrader := newWSUpgrader([]string{"https://app.example.com"})

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics", nil)
	// No Origin header — same-origin WS connection.
	if !upgrader.CheckOrigin(req) {
		t.Error("CheckOrigin: no Origin header (same-origin) should be allowed")
	}
}

// TestWS_BackendRetains_TokenQueryParam verifies INV-7:
// the backend tokenFromRequest still accepts ?token= (for CLI/script WS clients).
func TestWS_BackendRetains_TokenQueryParam(t *testing.T) {
	const tok = "cli-ws-token"
	// Build a request with only ?token= (no cookie, no bearer).
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics?token="+tok, nil)

	candidate := tokenFromRequest(req)
	if candidate != tok {
		t.Errorf("tokenFromRequest: ?token= not extracted; got %q, want %q", candidate, tok)
	}
}

// TestWS_TokenPriority_BearerOverCookieOverQuery verifies FR-22:
// priority order is Bearer > cookie > query.
func TestWS_TokenPriority_BearerOverCookieOverQuery(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics?token=query-tok", nil)
	req.Header.Set("Authorization", "Bearer bearer-tok")
	req.AddCookie(&http.Cookie{Name: "auth", Value: "cookie-tok"})

	candidate := tokenFromRequest(req)
	if candidate != "bearer-tok" {
		t.Errorf("priority: expected bearer-tok, got %q", candidate)
	}

	// Without bearer, cookie wins.
	req2, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics?token=query-tok", nil)
	req2.AddCookie(&http.Cookie{Name: "auth", Value: "cookie-tok"})
	candidate2 := tokenFromRequest(req2)
	if candidate2 != "cookie-tok" {
		t.Errorf("priority: expected cookie-tok, got %q", candidate2)
	}

	// Without bearer and cookie, query wins.
	req3, _ := http.NewRequest(http.MethodGet, "http://localhost/ws/metrics?token=query-tok", nil)
	candidate3 := tokenFromRequest(req3)
	if !strings.Contains(candidate3, "query-tok") {
		t.Errorf("priority: expected query-tok, got %q", candidate3)
	}
}
