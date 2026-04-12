package web

import (
	"net/http"
	"time"
)

type statusResponse struct {
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
	resp := statusResponse{
		Name:          s.deps.Config.Agent.Name,
		Provider:      s.deps.Config.Provider.Type,
		Model:         s.deps.Config.Provider.Model,
		Channel:       s.deps.Config.Channel.Type,
		Uptime:        uptime.Round(time.Second).String(),
		Version:       s.deps.Version,
		UptimeSeconds: int64(uptime.Seconds()),
	}
	writeJSON(w, http.StatusOK, resp)
}
