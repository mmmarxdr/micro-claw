package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
	"daimon/internal/mcp"
	"daimon/internal/provider"
	"daimon/internal/rag"
	"daimon/internal/rag/metrics"
	"daimon/internal/skill"
	"daimon/internal/store"
	"daimon/internal/tool"
	"daimon/internal/web/modelcache"
)

// MCPManager is the interface for managing MCP servers.
type MCPManager interface {
	List(ctx context.Context) ([]mcp.ServerStatus, error)
	Add(ctx context.Context, cfg config.MCPServerConfig) error
	Remove(ctx context.Context, name string) error
	Test(ctx context.Context, cfg config.MCPServerConfig) ([]string, error)
}

// providerFactory constructs a Provider from a ProviderConfig.
// Abstracted for testability — tests inject a mock factory; production uses provider.NewFromConfig.
type providerFactory func(cfg config.ProviderConfig) (provider.Provider, error)

// providerRegistry resolves a ModelLister by provider name.
// Implemented by *provider.Registry; tests inject a fake.
type providerRegistry interface {
	Lister(name string) (provider.ModelLister, bool)
	RegisterTransient(name string, p provider.Provider)
}

// AgentReloader is the slice of *agent.Agent the web layer needs to push
// hot-add events (new MCP server, new skill) into the running agent
// without requiring a daimon restart. The interface is defined here so
// the web package does not pull in agent.go's heavy import graph; the
// concrete *agent.Agent satisfies it via duck typing.
//
// Nil-safe: handlers always check `if s.deps.Agent != nil` before calling.
type AgentReloader interface {
	RegisterMCPServer(serverName string, tools map[string]tool.Tool, caller interface{ Close() error })
	UnregisterMCPServer(serverName string) error
	ReplaceSkills(skills []skill.SkillContent, idx skill.SkillIndex)
}

// ServerDeps holds the dependencies for the web server.
type ServerDeps struct {
	Store           store.Store
	Auditor         audit.Auditor
	Config          *config.Config
	ConfigPath      string               // resolved path to config.yaml (for MCP/skill operations)
	MCPService      MCPManager
	Agent           AgentReloader        // optional; enables hot-reload of MCP/skills from the dashboard
	ProviderRegistry providerRegistry      // nil until Phase 6 wiring is complete
	ModelCache       *modelcache.Cache     // nil until Phase 5 wiring is complete; handler creates a default if nil
	Tools            map[string]tool.Tool  // registered tool instances
	StartedAt       time.Time
	Version         string
	WebChannel      *channel.WebChannel // nil disables the /ws/chat endpoint
	MediaStore      store.MediaStore    // nil when media uploads are not configured
	ProviderFactory providerFactory     // nil defaults to provider.NewFromConfig

	// RAG — nil when the RAG subsystem is disabled. /api/knowledge endpoints
	// return 501 Not Implemented when DocStore is nil.
	DocStore     rag.DocumentStore
	IngestWorker *rag.DocIngestionWorker

	// RAGMetrics — nil when metrics collection is not configured.
	// GET /api/metrics/rag returns 501 when nil.
	RAGMetrics metrics.Recorder
}

// Server is the HTTP dashboard server.
type Server struct {
	deps        ServerDeps
	srv         *http.Server
	mux         *http.ServeMux
	wsUpgrader  websocket.Upgrader
	rateLimiter *ipRateLimiter
	// configMu guards all access to s.deps.Config. Readers call config() which
	// holds RLock and returns a snapshot; writers (handlePutConfig, rotateAuthToken,
	// setup handlers) hold the full Lock. *s.deps.Config = merged is a multi-word
	// struct assignment that is NOT atomic — readers observing a torn write would
	// see a mix of old and new field values, which is a confirmed data race under -race.
	configMu sync.RWMutex

	// auditorMu guards hot-swaps of s.deps.Auditor. Reads (RLock) happen on
	// each /ws/logs connection so they must be fast; the only writer is
	// rebuildAuditor invoked from PUT /api/config when the audit subtree
	// changes. Existing connections keep the auditor reference they grabbed
	// at handshake — they are not interrupted by a swap.
	auditorMu sync.RWMutex

	// Conversation soft-delete pruner — nil when the store does not
	// implement ConvPruneStore OR when disabled in config. Start/Shutdown
	// drive its lifecycle.
	convPruner *store.ConversationPruner
}

// NewServer creates a new Server with all routes registered.
// The auth token must be set in deps.Config.Web.AuthToken before calling.
func NewServer(deps ServerDeps) *Server {
	s := &Server{
		deps:       deps,
		mux:        http.NewServeMux(),
		wsUpgrader: newWSUpgrader(deps.Config.Web.AllowedOrigins),
	}
	s.routes()

	var handler http.Handler = s.mux

	// Body size limit on POST/PUT/PATCH requests.
	handler = bodySizeLimitMiddleware(defaultMaxBodySize, handler)

	// Auth — protects /api/* and /ws/* endpoints.
	// Both accessors read from config on every request (INV-1, INV-8): never captured
	// at startup so token rotation and IssuedAt updates are observed immediately.
	// s.config() returns a snapshot under RLock so these closures never observe
	// a torn struct from a concurrent handlePutConfig write.
	handler = authMiddlewareDynamic(
		func() string    { return s.config().Web.AuthToken },
		func() time.Time { return s.config().Web.AuthTokenIssuedAt },
		handler,
	)

	// CORS — allow same-origin by default; configure allowed_origins for cross-origin.
	handler = corsMiddleware(deps.Config.Web.AllowedOrigins, handler)

	// Per-IP rate limiting: 120 requests per minute on API/WS endpoints.
	s.rateLimiter = newIPRateLimiter(120, time.Minute)
	handler = rateLimitMiddleware(s.rateLimiter, deps.Config.Web.TrustProxy, handler)

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

// newWSUpgrader builds a websocket.Upgrader that validates the request origin
// against allowedOrigins.
//
// Cross-origin mode (allowedOrigins non-empty): only origins in the list are
// accepted; "*" is rejected as a wildcard — it would pair with
// Allow-Credentials which is prohibited (INV-5, FR-35).
//
// Same-origin mode (allowedOrigins empty): the Origin header MUST match the
// request Host (scheme + host + port). Requests with no Origin header (e.g.
// same-origin native WS from the same browser tab) are always allowed.
// This replaces the old allow-all default (T-035/T-036, FR-35).
func newWSUpgrader(allowedOrigins []string) websocket.Upgrader {
	originSet := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "*" { // wildcard explicitly excluded (INV-5)
			originSet[strings.TrimRight(o, "/")] = true
		}
	}
	crossOriginMode := len(originSet) > 0

	return websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// No Origin header — same-origin browser request or non-browser
				// CLI client. Allow in both modes (FR-35, T-164).
				return true
			}
			if crossOriginMode {
				return originSet[strings.TrimRight(origin, "/")]
			}
			// Same-origin mode: origin must match the request host.
			// The gorilla/websocket default was allow-all; we enforce host match.
			host := r.Host
			if host == "" {
				host = r.URL.Host
			}
			// Compare scheme+host+port. Origin may or may not have a trailing slash.
			return strings.TrimRight(origin, "/") == ("http://"+host) ||
				strings.TrimRight(origin, "/") == ("https://"+host)
		},
	}
}

// Start begins listening in a background goroutine.
// If TLS cert and key are configured, it starts with TLS.
func (s *Server) Start() error {
	// Wire the conversation pruner when the store supports it AND config
	// enables it. The narrow ConvPruneStore interface means FileStore and
	// other non-SQLite backends simply skip this — no pruner goroutine
	// runs, which is the correct behavior for stores that don't support
	// soft delete.
	if pruneStore, ok := s.deps.Store.(store.ConvPruneStore); ok {
		cfg := s.config()
		pc := cfg.Conversations.Prune
		s.convPruner = store.NewConversationPruner(pruneStore, store.SystemClock{},
			store.PrunerConfig{
				Enabled:   pc.Enabled,
				Retention: time.Duration(pc.RetentionDays) * 24 * time.Hour,
				Interval:  time.Duration(pc.IntervalHours) * time.Hour,
			})
		s.convPruner.Start(context.Background())
	}

	go func() {
		cfg := s.config()
		var err error
		if cfg.Web.TLSCert != "" && cfg.Web.TLSKey != "" {
			err = s.srv.ListenAndServeTLS(cfg.Web.TLSCert, cfg.Web.TLSKey)
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
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
	if s.convPruner != nil {
		s.convPruner.Stop()
	}
	return s.srv.Shutdown(ctx)
}

// config returns a shallow snapshot of the current server config under RLock.
// Callers use the returned value for the duration of a single request; they
// must NOT store the snapshot across requests (it becomes stale on the next PUT).
//
// Use this in every read-only handler instead of accessing s.deps.Config.*
// directly, to avoid observing a torn struct from the multi-word assignment in
// handlePutConfig.
func (s *Server) config() config.Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return *s.deps.Config
}

// mediaStore returns the MediaStore if media uploads are enabled, otherwise nil.
// Callers should check for nil before using the store.
func (s *Server) mediaStore() store.MediaStore {
	if s.deps.MediaStore == nil {
		return nil
	}
	if !config.BoolVal(s.config().Media.Enabled) {
		return nil
	}
	return s.deps.MediaStore
}

// routes registers all HTTP routes.
func (s *Server) routes() {
	ao := s.config().Web.AllowedOrigins // alias for readability

	// Setup endpoints — always accessible, bypass auth middleware via authMiddlewareDynamic exemption.
	s.mux.HandleFunc("GET /api/setup/status", s.handleGetSetupStatus)
	s.mux.HandleFunc("GET /api/setup/providers", s.handleGetSetupProviders)
	// POST /api/setup/validate-key and /api/setup/complete bypass auth (pre-setup flow).
	// POST /api/setup/reset requires auth (guarded by the auth middleware — NOT in exempt list).
	s.mux.HandleFunc("POST /api/setup/validate-key", s.handleValidateKey)
	s.mux.HandleFunc("POST /api/setup/complete", s.handleSetupComplete)
	s.mux.Handle("POST /api/setup/reset", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleSetupReset)))

	// Auth endpoints.
	// POST /api/auth/login is EXEMPT from auth middleware (FR-15) via the exemption in
	// authMiddlewareDynamic. No requireOriginIfCrossOrigin — login is the entry point.
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	// POST /api/auth/logout is BEHIND auth middleware (FR-16) and requires origin validation.
	s.mux.Handle("POST /api/auth/logout", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleLogout)))

	s.mux.HandleFunc("GET /api/status", s.handleGetStatus)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.Handle("PUT /api/config", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handlePutConfig)))
	s.mux.HandleFunc("GET /api/conversations", s.handleListConversations)
	s.mux.HandleFunc("GET /api/conversations/{id}", s.handleGetConversation)
	s.mux.HandleFunc("GET /api/conversations/{id}/messages", s.handleGetConversationMessages)
	s.mux.Handle("PATCH /api/conversations/{id}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handlePatchConversation)))
	s.mux.Handle("POST /api/conversations/{id}/restore", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleRestoreConversation)))
	s.mux.Handle("DELETE /api/conversations/{id}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleDeleteConversation)))
	s.mux.HandleFunc("GET /api/memory", s.handleListMemory)
	s.mux.Handle("POST /api/memory", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handlePostMemory)))
	s.mux.Handle("DELETE /api/memory/{id}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleDeleteMemory)))
	s.mux.HandleFunc("GET /api/knowledge", s.handleListKnowledge)
	s.mux.Handle("POST /api/knowledge", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handlePostKnowledge)))
	s.mux.Handle("DELETE /api/knowledge/{id}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleDeleteKnowledge)))
	s.mux.HandleFunc("GET /api/metrics", s.handleGetMetrics)
	s.mux.HandleFunc("GET /api/metrics/history", s.handleGetMetricsHistory)
	s.mux.HandleFunc("GET /api/metrics/rag", s.handleGetRAGMetrics)
	s.mux.HandleFunc("GET /api/system-metrics", s.handleGetSystemMetrics)
	s.mux.HandleFunc("GET /api/mcp/servers", s.handleListMCPServers)
	s.mux.Handle("POST /api/mcp/servers", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleAddMCPServer)))
	s.mux.Handle("DELETE /api/mcp/servers/{name}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleRemoveMCPServer)))
	s.mux.Handle("POST /api/mcp/servers/{name}/test", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleTestMCPServer)))
	s.mux.HandleFunc("GET /api/providers/{provider}/models", s.handleListProviderModels)
	s.mux.HandleFunc("GET /api/tools", s.handleListTools)
	// Media upload, retrieval, listing, and deletion endpoints.
	s.mux.Handle("POST /api/upload", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleUpload)))
	s.mux.HandleFunc("GET /api/media", s.handleListMedia)
	s.mux.HandleFunc("GET /api/media/{sha256}", s.handleGetMedia)
	s.mux.Handle("DELETE /api/media/{sha256}", requireOriginIfCrossOrigin(ao, http.HandlerFunc(s.handleDeleteMedia)))
	// WebSocket endpoints.
	s.mux.HandleFunc("/ws/metrics", s.handleMetricsWebSocket)
	s.mux.HandleFunc("/ws/logs", s.handleLogsWebSocket)
	// WebSocket chat endpoint — only when a WebChannel is wired in.
	if s.deps.WebChannel != nil {
		// Wire MediaStore so attachment SHA-256 references can be validated.
		if s.deps.MediaStore != nil && s.mediaStore() != nil {
			s.deps.WebChannel.SetMediaStore(s.deps.MediaStore)
		}
		s.mux.HandleFunc("/ws/chat", s.deps.WebChannel.HandleWebSocket)
	}
	// Static files with SPA fallback — catch-all.
	s.mux.Handle("/", s.staticHandler())
}
