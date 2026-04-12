package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
