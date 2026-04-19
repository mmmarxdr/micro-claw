package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("expected 64-char hex token, got %d chars", len(tok))
	}
	// Must be unique.
	tok2, _ := GenerateToken()
	if tok == tok2 {
		t.Fatal("two generated tokens are identical")
	}
}

func TestTokenMatch(t *testing.T) {
	if tokenMatch("abc", "abc") != true {
		t.Fatal("same tokens should match")
	}
	if tokenMatch("abc", "xyz") != false {
		t.Fatal("different tokens should not match")
	}
	if tokenMatch("", "abc") != false {
		t.Fatal("empty candidate should not match")
	}
	if tokenMatch("abc", "") != false {
		t.Fatal("empty expected should not match")
	}
}

func TestAuthMiddleware_BearerHeader(t *testing.T) {
	const token = "test-secret-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddleware(token, inner)

	// Valid token in Authorization header.
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid bearer, got %d", rec.Code)
	}
}

func TestAuthMiddleware_QueryParam(t *testing.T) {
	const token = "test-secret-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddleware(token, inner)

	// Valid token in query param (WebSocket path).
	req := httptest.NewRequest("GET", "/ws/chat?token="+token, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid query token, got %d", rec.Code)
	}
}

func TestAuthMiddleware_Unauthorized(t *testing.T) {
	const token = "test-secret-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddleware(token, inner)

	tests := []struct {
		name string
		path string
		auth string
	}{
		{"no token on api", "/api/status", ""},
		{"wrong token on api", "/api/config", "Bearer wrong-token"},
		{"no token on ws", "/ws/metrics", ""},
		{"wrong query token on ws", "/ws/chat?token=wrong", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}
}

// TestAuth_DynamicTokenAccessor verifies INV-1/INV-2: authMiddlewareDynamic reads
// the token from the tokenFn closure on every request, so a mid-test mutation of
// the underlying value is immediately observed (regression for 66e5323 bug).
// This test also validates the new dual-accessor signature (tokenFn + issuedAtFn).
func TestAuth_DynamicTokenAccessor(t *testing.T) {
	token := "initial-token"
	issuedAt := time.Now() // within TTL

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return issuedAt },
		inner,
	)

	// First request with initial token — should pass.
	req1 := httptest.NewRequest("GET", "/api/status", nil)
	req1.Header.Set("Authorization", "Bearer "+token)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 with initial token, got %d", rec1.Code)
	}

	// Mutate the token mid-test (simulates token rotation).
	token = "rotated-token"

	// Old token must now be rejected (middleware reads tokenFn fresh).
	req2 := httptest.NewRequest("GET", "/api/status", nil)
	req2.Header.Set("Authorization", "Bearer initial-token")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after rotation with old token, got %d", rec2.Code)
	}

	// New token must be accepted.
	req3 := httptest.NewRequest("GET", "/api/status", nil)
	req3.Header.Set("Authorization", "Bearer rotated-token")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 with new token, got %d", rec3.Code)
	}
}

// TestAuth_DynamicIssuedAtAccessor verifies INV-8: the middleware reads
// AuthTokenIssuedAt dynamically (via issuedAtFn) on every request — not captured
// at construction time. A mid-test mutation must be observed by the next request.
func TestAuth_DynamicIssuedAtAccessor(t *testing.T) {
	token := "tok"
	issuedAt := time.Now() // fresh — within TTL

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return issuedAt },
		inner,
	)

	// First request with fresh IssuedAt — should pass.
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with fresh IssuedAt, got %d", rec.Code)
	}

	// Mutate issuedAt to 31 days ago (expired).
	issuedAt = time.Now().Add(-31 * 24 * time.Hour)

	// Subsequent request must be rejected with 401 (TTL expired).
	req2 := httptest.NewRequest("GET", "/api/status", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after IssuedAt mutation to expired, got %d", rec2.Code)
	}
}

// TestAuth_TTLExpired_Returns401AndClearsCookie verifies FR-57/AS-22/INV-9:
// a cookie with a valid token but expired IssuedAt (31 days) returns 401 + clear-cookie.
func TestAuth_TTLExpired_Returns401AndClearsCookie(t *testing.T) {
	token := "valid-token"
	expiredIssuedAt := time.Now().Add(-31 * 24 * time.Hour)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return expiredIssuedAt },
		inner,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.AddCookie(&http.Cookie{Name: "auth", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired TTL, got %d", rec.Code)
	}
	// Must include a Set-Cookie that clears the auth cookie (MaxAge=0).
	resp := &http.Response{Header: rec.Header()}
	var clearedCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "auth" {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil {
		t.Fatal("expected Set-Cookie: auth= to clear expired cookie, got none")
	}
	if clearedCookie.MaxAge != 0 {
		t.Errorf("clear cookie MaxAge: got %d, want 0", clearedCookie.MaxAge)
	}
}

// TestAuth_TTLExactBoundary_Passes verifies that a cookie exactly at TTL boundary
// (time.Since(issuedAt) == authCookieTTL) still passes (strict > boundary).
func TestAuth_TTLExactBoundary_Passes(t *testing.T) {
	token := "tok"
	// Set IssuedAt to exactly authCookieTTL ago. Due to execution time the actual
	// time.Since will be slightly above, so we use authCookieTTL - 1ms to simulate
	// "just at boundary" without depending on nanosecond timing.
	issuedAt := time.Now().Add(-authCookieTTL + time.Millisecond)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return issuedAt },
		inner,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at TTL boundary, got %d", rec.Code)
	}
}

// TestAuth_TTLOnePastBoundary_Rejects verifies that one nanosecond past TTL is rejected.
func TestAuth_TTLOnePastBoundary_Rejects(t *testing.T) {
	token := "tok"
	// One full millisecond past TTL to avoid timing sensitivity.
	issuedAt := time.Now().Add(-authCookieTTL - time.Millisecond)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return token },
		func() time.Time { return issuedAt },
		inner,
	)

	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 one past TTL, got %d", rec.Code)
	}
}

// TestAuth_PreSetupBypass verifies INV-2/FR-23: when tokenFn returns "", all
// requests pass regardless of credentials (pre-setup mode).
func TestAuth_PreSetupBypass(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddlewareDynamic(
		func() string    { return "" }, // no token configured
		func() time.Time { return time.Time{} },
		inner,
	)

	req := httptest.NewRequest("GET", "/api/status", nil) // no auth at all
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 in pre-setup mode (no token), got %d", rec.Code)
	}
}

func TestAuthMiddleware_StaticBypass(t *testing.T) {
	const token = "test-secret-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddleware(token, inner)

	// Static/SPA routes should NOT require auth.
	paths := []string{"/", "/index.html", "/assets/index.js", "/favicon.svg"}
	for _, path := range paths {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s without auth, got %d", path, rec.Code)
		}
	}
}
