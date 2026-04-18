package web

import (
	"net/http"
	"time"

	"microagent/internal/config"
)

type statusResponse struct {
	Status        string `json:"status"` // "running" | "idle" | "error"
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Channel       string `json:"channel"`
	Uptime        string `json:"uptime"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

func (s *Server) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.deps.StartedAt)
	// Use ResolveActiveProvider to read the active provider from the v2 shape.
	activeProv := config.ResolveActiveProvider(*s.deps.Config)
	resp := statusResponse{
		Status:        "running",
		Name:          s.deps.Config.Agent.Name,
		Provider:      activeProv.Type,
		Model:         activeProv.Model,
		Channel:       s.deps.Config.Channel.Type,
		Uptime:        uptime.Round(time.Second).String(),
		Version:       s.deps.Version,
		UptimeSeconds: int64(uptime.Seconds()),
	}
	writeJSON(w, http.StatusOK, resp)
}
