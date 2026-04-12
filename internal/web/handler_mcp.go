package web

import (
	"net/http"
	"strings"
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
