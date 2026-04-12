package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"microagent/internal/audit"
	"microagent/internal/config"
	"microagent/internal/mcp"
	"microagent/internal/store"
)

// MCPLister is the interface for listing MCP server statuses.
type MCPLister interface {
	List(ctx context.Context) ([]mcp.ServerStatus, error)
}

// ServerDeps holds the dependencies for the web server.
type ServerDeps struct {
	Store      store.Store
	Auditor    audit.Auditor
	Config     *config.Config
	MCPService MCPLister
	StartedAt  time.Time
	Version    string
}

// Server is the HTTP dashboard server.
type Server struct {
	deps ServerDeps
	srv  *http.Server
	mux  *http.ServeMux
}

// NewServer creates a new Server with all routes registered.
func NewServer(deps ServerDeps) *Server {
	s := &Server{
		deps: deps,
		mux:  http.NewServeMux(),
	}
	s.routes()
	handler := loggingMiddleware(recoveryMiddleware(s.mux))
	s.srv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", deps.Config.Web.Host, deps.Config.Web.Port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// Start begins listening in a background goroutine.
func (s *Server) Start() error {
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Log but do not crash — caller is responsible for shutdown.
			_ = err
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
	// Static files with SPA fallback — catch-all.
	s.mux.Handle("/", s.staticHandler())
}
