package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"daimon/internal/config"
	"daimon/internal/mcp"
)

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.deps.MCPService == nil {
		writeJSON(w, http.StatusOK, map[string]any{"servers": []any{}})
		return
	}

	servers, err := s.deps.MCPService.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type mcpServer struct {
		Name      string `json:"name"`
		Transport string `json:"transport"`
		Command   string `json:"command,omitempty"`
		URL       string `json:"url,omitempty"`
		Connected bool   `json:"connected"`
		ToolCount int    `json:"tool_count"`
	}

	result := make([]mcpServer, 0, len(servers))
	for _, srv := range servers {
		ms := mcpServer{
			Name:      srv.Config.Name,
			Transport: srv.Config.Transport,
			URL:       srv.Config.URL,
			Connected: srv.Connected,
			ToolCount: srv.ToolCount,
		}
		if len(srv.Config.Command) > 0 {
			ms.Command = strings.Join(srv.Config.Command, " ")
		}
		result = append(result, ms)
	}

	writeJSON(w, http.StatusOK, map[string]any{"servers": result})
}

func (s *Server) handleAddMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.deps.MCPService == nil {
		writeError(w, http.StatusServiceUnavailable, "MCP service not available")
		return
	}

	var cfg config.MCPServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := s.deps.MCPService.Add(r.Context(), cfg); err != nil {
		if errors.Is(err, mcp.ErrDuplicateName) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Hot-add into the running agent so the new tools are usable on the
	// next turn without a daimon restart. Failures here log a warning but
	// do not undo the persistent Add — user can still restart to pick it up.
	if s.deps.Agent != nil {
		hotAddCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		tools, caller, hotErr := mcp.ConnectSingleServer(hotAddCtx, cfg)
		cancel()
		if hotErr != nil {
			slog.Warn("hot-add: connect failed (server saved, requires restart to use)",
				"server", cfg.Name, "error", hotErr)
		} else {
			s.deps.Agent.RegisterMCPServer(cfg.Name, tools, caller)
		}
	}

	// Auto-install bundled skill for known MCP recipes.
	if s.deps.ConfigPath != "" {
		cfgSnap := s.config()
		installRecipeSkill(cfg.Name, &cfgSnap, s.deps.ConfigPath, s.deps.Agent)
	}

	writeJSON(w, http.StatusCreated, cfg)
}

func (s *Server) handleRemoveMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.deps.MCPService == nil {
		writeError(w, http.StatusServiceUnavailable, "MCP service not available")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "server name is required")
		return
	}

	if err := s.deps.MCPService.Remove(r.Context(), name); err != nil {
		if errors.Is(err, mcp.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Hot-unregister from the running agent. A NotFound here is expected
	// for boot-time servers (we don't track those in the hot-add registry)
	// — it just means the tools stay live until restart, harmless.
	if s.deps.Agent != nil {
		if err := s.deps.Agent.UnregisterMCPServer(name); err != nil {
			slog.Debug("hot-remove: not in hot-add registry (server was added at boot)",
				"server", name)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.deps.MCPService == nil {
		writeError(w, http.StatusServiceUnavailable, "MCP service not available")
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "server name is required")
		return
	}

	servers, err := s.deps.MCPService.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var found *config.MCPServerConfig
	for i := range servers {
		if servers[i].Config.Name == name {
			found = &servers[i].Config
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "server not found: "+name)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tools, err := s.deps.MCPService.Test(ctx, *found)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true,
		"tools":     tools,
	})
}
