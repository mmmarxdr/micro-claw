package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newSlogCapture creates a slog.Logger backed by a bytes.Buffer using
// TextHandler so we can scan for structured key=value pairs in tests.
func newSlogCapture() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// containsAll checks that all substrings are present in s.
func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------------
// W-1: authMiddlewareDynamic must log WARN on 401 with reason codes
// ----------------------------------------------------------------------------

// TestMiddleware_401_NoToken_LogsWarn verifies NFR-3: when no token is present
// in the request (but a token IS configured), middleware logs WARN with reason=no-token.
func TestMiddleware_401_NoToken_LogsWarn(t *testing.T) {
	logger, buf := newSlogCapture()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamicWithLogger(
		func() string    { return "configured-token" },
		func() time.Time { return time.Now() }, // fresh — within TTL
		inner,
		logger,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	logged := buf.String()
	if !containsAll(logged, "WARN", "no-token", "127.0.0.1:9999", "/api/status") {
		t.Errorf("expected WARN log with reason=no-token, remote_addr and path; got: %q", logged)
	}
}

// TestMiddleware_401_BadToken_LogsWarn verifies NFR-3: when a token is present
// but doesn't match, middleware logs WARN with reason=bad-token.
func TestMiddleware_401_BadToken_LogsWarn(t *testing.T) {
	logger, buf := newSlogCapture()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamicWithLogger(
		func() string    { return "real-token" },
		func() time.Time { return time.Now() },
		inner,
		logger,
	)

	req := httptest.NewRequest("GET", "/api/config", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.RemoteAddr = "10.0.0.1:5050"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	logged := buf.String()
	if !containsAll(logged, "WARN", "bad-token", "10.0.0.1:5050", "/api/config") {
		t.Errorf("expected WARN log with reason=bad-token, remote_addr and path; got: %q", logged)
	}
	// Token value must NOT be logged.
	if strings.Contains(logged, "wrong-token") {
		t.Errorf("token value must not appear in log output; got: %q", logged)
	}
}

// TestMiddleware_401_TtlExpired_LogsWarn verifies NFR-3: when a token matches
// but TTL is expired, middleware logs WARN with reason=ttl-expired.
func TestMiddleware_401_TtlExpired_LogsWarn(t *testing.T) {
	logger, buf := newSlogCapture()
	token := "valid-token"
	expiredIssuedAt := time.Now().Add(-31 * 24 * time.Hour)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamicWithLogger(
		func() string    { return token },
		func() time.Time { return expiredIssuedAt },
		inner,
		logger,
	)

	req := httptest.NewRequest("GET", "/api/ws", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: token})
	req.RemoteAddr = "192.168.1.1:8888"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	logged := buf.String()
	if !containsAll(logged, "WARN", "ttl-expired", "192.168.1.1:8888") {
		t.Errorf("expected WARN log with reason=ttl-expired, remote_addr; got: %q", logged)
	}
}

// TestMiddleware_NoToken_WithConfiguredToken_Logs verifies that no-token path
// only logs when a token IS configured (pre-setup mode must NOT log — INV-2).
func TestMiddleware_NoToken_PreSetup_NoLog(t *testing.T) {
	logger, buf := newSlogCapture()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Pre-setup: tokenFn returns ""
	handler := authMiddlewareDynamicWithLogger(
		func() string    { return "" },
		func() time.Time { return time.Time{} },
		inner,
		logger,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 in pre-setup, got %d", rec.Code)
	}
	if buf.Len() > 0 {
		t.Errorf("pre-setup bypass must NOT log anything; got: %q", buf.String())
	}
}

// ----------------------------------------------------------------------------
// W-2: rotateAuthToken must log per-phase
// ----------------------------------------------------------------------------

// TestRotateAuthToken_LogsAllPhases verifies NFR-3: rotateAuthToken emits
// per-phase log entries (disk-write-complete, memory-updated, issued-at-stamped).
func TestRotateAuthToken_LogsAllPhases(t *testing.T) {
	logger, buf := newSlogCapture()
	s, _ := newServerForRotation(t)

	_, err := s.rotateAuthTokenWithLogger(context.Background(), logger)
	if err != nil {
		t.Fatalf("unexpected rotation error: %v", err)
	}

	logged := buf.String()
	phases := []string{
		"auth: rotation disk-write-complete",
		"auth: rotation memory-updated",
		"auth: rotation issued-at-stamped",
	}
	for _, phase := range phases {
		if !strings.Contains(logged, phase) {
			t.Errorf("missing log phase %q in output:\n%s", phase, logged)
		}
	}
}

// TestRotateAuthToken_DiskFailure_LogsError verifies NFR-3: when AtomicWriteConfig
// fails, an error log is emitted before returning.
func TestRotateAuthToken_DiskFailure_LogsError(t *testing.T) {
	logger, buf := newSlogCapture()
	s, _ := newServerForRotation(t)
	s.deps.ConfigPath = "/non-existent-dir/config.yaml"

	_, err := s.rotateAuthTokenWithLogger(context.Background(), logger)
	if err == nil {
		t.Fatal("expected error from disk failure, got nil")
	}

	logged := buf.String()
	if !containsAll(logged, "ERROR", "auth: rotation disk-write-failed") {
		t.Errorf("expected ERROR log for disk-write-failed; got: %q", logged)
	}
}

// ----------------------------------------------------------------------------
// S-1: clearStaleAuthCookie must mirror SameSite/Secure from config
// ----------------------------------------------------------------------------

// TestMiddleware_StaleCookieClear_MirrorsSameSiteNone_InCrossOriginMode verifies S-1:
// in cross-origin mode (AllowedOrigins set), the eviction cookie has SameSite=None; Secure.
func TestMiddleware_StaleCookieClear_MirrorsSameSiteNone_InCrossOriginMode(t *testing.T) {
	token := "valid-token"
	expiredIssuedAt := time.Now().Add(-31 * 24 * time.Hour)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cfg := minimalConfig()
	cfg.Web.AllowedOrigins = []string{"https://app.example.com"}

	handler := authMiddlewareDynamicWithConfig(
		func() string    { return token },
		func() time.Time { return expiredIssuedAt },
		inner,
		&cfg.Web,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	resp := &http.Response{Header: rec.Header()}
	var cleared *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "auth" {
			cleared = c
			break
		}
	}
	if cleared == nil {
		t.Fatal("expected Set-Cookie: auth= eviction, got none")
	}
	if cleared.MaxAge != 0 {
		t.Errorf("MaxAge: got %d, want 0", cleared.MaxAge)
	}
	if cleared.SameSite != http.SameSiteNoneMode {
		t.Errorf("SameSite: got %v, want SameSiteNone", cleared.SameSite)
	}
	if !cleared.Secure {
		t.Error("Secure flag: expected true in cross-origin mode")
	}
}

// TestMiddleware_StaleCookieClear_MirrorsStrict_InSameOriginMode verifies S-1:
// in same-origin mode (no AllowedOrigins), the eviction cookie has SameSite=Strict.
func TestMiddleware_StaleCookieClear_MirrorsStrict_InSameOriginMode(t *testing.T) {
	token := "valid-token"
	expiredIssuedAt := time.Now().Add(-31 * 24 * time.Hour)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cfg := minimalConfig()
	// No AllowedOrigins — same-origin mode.

	handler := authMiddlewareDynamicWithConfig(
		func() string    { return token },
		func() time.Time { return expiredIssuedAt },
		inner,
		&cfg.Web,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	resp := &http.Response{Header: rec.Header()}
	var cleared *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "auth" {
			cleared = c
			break
		}
	}
	if cleared == nil {
		t.Fatal("expected Set-Cookie: auth= eviction, got none")
	}
	if cleared.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite: got %v, want SameSiteStrict", cleared.SameSite)
	}
	if cleared.MaxAge != 0 {
		t.Errorf("MaxAge: got %d, want 0", cleared.MaxAge)
	}
}
