package web

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
// It also implements http.Hijacker so WebSocket upgrades work through middleware.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration", time.Since(start),
		)
	})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("http handler panic", "recover", rec, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------------
// Security headers
// --------------------------------------------------------------------------

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------------
// Request body size limit
// --------------------------------------------------------------------------

const defaultMaxBodySize = 1 << 20 // 1 MB

func bodySizeLimitMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------------
// CORS
// --------------------------------------------------------------------------

// corsMiddleware handles Cross-Origin Resource Sharing.
//
// Security rules (FR-30..FR-32, INV-5):
//   - AllowedOrigins empty (same-origin mode): no CORS headers emitted.
//     Cross-origin browsers get no ACAO, which effectively blocks them — this
//     is correct because the deployment is same-origin only.
//   - AllowedOrigins non-empty (cross-origin mode): echo the request Origin only
//     when it is in the allowlist; emit Allow-Credentials: true only alongside
//     the echoed origin (never with a wildcard "*").
//   - Wildcard "*" in AllowedOrigins is treated as an explicit origin entry, not
//     as "all origins allowed", preserving the no-wildcard+credentials invariant.
func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	crossOriginMode := len(allowedOrigins) > 0
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		originSet[strings.TrimRight(o, "/")] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// No Origin header → simple request from same origin or non-browser client.
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Same-origin mode: emit no CORS headers (FR-31 — no Allow-Credentials).
		if !crossOriginMode {
			if r.Method == http.MethodOptions {
				// Preflight in same-origin mode is unusual but we handle it gracefully.
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Cross-origin mode: check allowlist.
		allowed := originSet[strings.TrimRight(origin, "/")]
		if allowed {
			// FR-30: echo the exact origin (never wildcard) + credentials header.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "3600")
		}

		if r.Method == http.MethodOptions {
			if allowed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------------------
// Per-IP rate limiter
// --------------------------------------------------------------------------

type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*rateBucket
	rate     int           // max requests per window
	window   time.Duration // sliding window size
	done     chan struct{}
	stopOnce sync.Once
}

type rateBucket struct {
	count    int
	windowAt time.Time
}

func newIPRateLimiter(rate int, window time.Duration) *ipRateLimiter {
	rl := &ipRateLimiter{
		visitors: make(map[string]*rateBucket),
		rate:     rate,
		window:   window,
		done:     make(chan struct{}),
	}
	// Periodic cleanup of stale entries.
	ticker := time.NewTicker(window * 2)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				rl.cleanup()
			case <-rl.done:
				return
			}
		}
	}()
	return rl
}

// Stop shuts down the background cleanup goroutine. Safe to call multiple times.
func (rl *ipRateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.done) })
}

func (rl *ipRateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b, ok := rl.visitors[ip]
	if !ok || now.Sub(b.windowAt) > rl.window {
		rl.visitors[ip] = &rateBucket{count: 1, windowAt: now}
		return true
	}
	b.count++
	return b.count <= rl.rate
}

func (rl *ipRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-rl.window * 2)
	for ip, b := range rl.visitors {
		if b.windowAt.Before(cutoff) {
			delete(rl.visitors, ip)
		}
	}
}

func rateLimitMiddleware(limiter *ipRateLimiter, trustProxy bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only rate-limit API and WebSocket endpoints.
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}
		ip := extractIP(r, trustProxy)
		if !limiter.allow(ip) {
			slog.Warn("rate limit exceeded", "ip", ip, "path", path)
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP returns the client IP address from the request. When trustProxy is
// true, the X-Forwarded-For header is trusted and the leftmost IP is used (set
// by a trusted reverse proxy). When trustProxy is false (the default), only
// RemoteAddr is used to prevent IP spoofing via header injection.
func extractIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if ip, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(ip)
			}
			return strings.TrimSpace(xff)
		}
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}
