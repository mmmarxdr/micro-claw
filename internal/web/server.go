package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/mcp"
	"microagent/internal/provider"
	"microagent/internal/store"
	"microagent/internal/tool"
)

// MCPManager is the interface for managing MCP servers.
type MCPManager interface {
	List(ctx context.Context) ([]mcp.ServerStatus, error)
	Add(ctx context.Context, cfg config.MCPServerConfig) error
	Remove(ctx context.Context, name string) error
	Test(ctx context.Context, cfg config.MCPServerConfig) ([]string, error)
}

// ServerDeps holds the dependencies for the web server.
type ServerDeps struct {
	Store       store.Store
	Auditor     audit.Auditor
	Config      *config.Config
	ConfigPath  string                 // resolved path to config.yaml (for MCP/skill operations)
	MCPService  MCPManager
	ModelLister provider.ModelLister   // nil if provider doesn't support model listing
	Tools       map[string]tool.Tool   // registered tool instances
	StartedAt   time.Time
	Version     string
	WebChannel  *channel.WebChannel // nil disables the /ws/chat endpoint
}

// Server is the HTTP dashboard server.
type Server struct {
	deps ServerDeps
	srv  *http.Server
	mux  *http.ServeMux
}

// NewServer creates a new Server with all routes registered.
// The auth token must be set in deps.Config.Web.AuthToken before calling.
func NewServer(deps ServerDeps) *Server {
	s := &Server{
		deps: deps,
		mux:  http.NewServeMux(),
	}
	s.routes()

	var handler http.Handler = s.mux

	// Body size limit on POST/PUT/PATCH requests.
	handler = bodySizeLimitMiddleware(defaultMaxBodySize, handler)

	// Auth — protects /api/* and /ws/* endpoints.
	if token := deps.Config.Web.AuthToken; token != "" {
		handler = authMiddleware(token, handler)
	}

	// CORS — allow same-origin by default; configure allowed_origins for cross-origin.
	handler = corsMiddleware(deps.Config.Web.AllowedOrigins, handler)

	// Per-IP rate limiting: 120 requests per minute on API/WS endpoints.
	limiter := newIPRateLimiter(120, time.Minute)
	handler = rateLimitMiddleware(limiter, handler)

	// Security headers on all responses.
	handler = securityHeadersMiddleware(handler)

	// Recovery + logging — outermost layers.
	handler = loggingMiddleware(recoveryMiddleware(handler))

	s.srv = &http.Server{
		Addr:           fmt.Sprintf("%s:%d", deps.Config.Web.Host, deps.Config.Web.Port),
		Handler:        handler,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   60 * time.Second, // increased for streaming WS responses
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB max header size
	}
	return s
}

// Start begins listening in a background goroutine.
// If TLS cert and key are configured, it starts with TLS.
func (s *Server) Start() error {
	go func() {
		var err error
		if s.deps.Config.Web.TLSCert != "" && s.deps.Config.Web.TLSKey != "" {
			err = s.srv.ListenAndServeTLS(s.deps.Config.Web.TLSCert, s.deps.Config.Web.TLSKey)
		} else {
			err = s.srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("web server error", "error", err)
		}
	}()
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// routes registers all HTTP routes.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/status", s.handleGetStatus)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	s.mux.HandleFunc("GET /api/conversations/{id}", s.handleGetConversation)
	s.mux.HandleFunc("DELETE /api/conversations/{id}", s.handleDeleteConversation)
	s.mux.HandleFunc("GET /api/memory", s.handleListMemory)
	s.mux.HandleFunc("POST /api/memory", s.handlePostMemory)
	s.mux.HandleFunc("DELETE /api/memory/{id}", s.handleDeleteMemory)
	s.mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)
	s.mux.HandleFunc("GET /api/metrics/history", s.handleGetMetricsHistory)
	s.mux.HandleFunc("GET /api/mcp/servers", s.handleListMCPServers)
	s.mux.HandleFunc("POST /api/mcp/servers", s.handleAddMCPServer)
	s.mux.HandleFunc("DELETE /api/mcp/servers/{name}", s.handleRemoveMCPServer)
	s.mux.HandleFunc("POST /api/mcp/servers/{name}/test", s.handleTestMCPServer)
	s.mux.HandleFunc("GET /api/models", s.handleListModels)
	s.mux.HandleFunc("GET /api/tools", s.handleListTools)
	// WebSocket endpoints.
	s.mux.HandleFunc("/ws/metrics", s.handleMetricsWebSocket)
	s.mux.HandleFunc("/ws/logs", s.handleLogsWebSocket)
	// WebSocket chat endpoint — only when a WebChannel is wired in.
	if s.deps.WebChannel != nil {
		s.mux.HandleFunc("/ws/chat", s.deps.WebChannel.HandleWebSocket)
	}
	// Static files with SPA fallback — catch-all.
	s.mux.Handle("/", s.staticHandler())
}
