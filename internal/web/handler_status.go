package web

import (
	"net/http"
	"time"

	"daimon/internal/config"
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
	cfg := s.config()
	// Use ResolveActiveProvider to read the active provider from the v2 shape.
	activeProv := config.ResolveActiveProvider(cfg)
	resp := statusResponse{
		Status:        "running",
		Name:          cfg.Agent.Name,
		Provider:      activeProv.Type,
		Model:         activeProv.Model,
		Channel:       cfg.Channel.Type,
		Uptime:        uptime.Round(time.Second).String(),
		Version:       s.deps.Version,
		UptimeSeconds: int64(uptime.Seconds()),
	}
	writeJSON(w, http.StatusOK, resp)
}
