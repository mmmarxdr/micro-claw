package web

import (
	"context"
	"encoding/json"
	"errors"
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

	// Auto-install bundled skill for known MCP recipes.
	if s.deps.ConfigPath != "" {
		installRecipeSkill(cfg.Name, s.deps.Config, s.deps.ConfigPath)
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
