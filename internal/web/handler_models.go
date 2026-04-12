package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.deps.ModelLister == nil {
		// Provider doesn't support model listing — return the static catalog.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}

	models, err := s.deps.ModelLister.ListModels(r.Context())
	if err != nil {
		slog.Error("failed to list models", "error", err)
		http.Error(w, `{"error":"failed to fetch models"}`, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(models); err != nil {
		slog.Error("failed to encode models", "error", err)
	}
}
