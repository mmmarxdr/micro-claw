package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"microagent/internal/store"
)

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	scopeID := r.URL.Query().Get("scope")

	entries, err := s.deps.Store.SearchMemory(r.Context(), scopeID, q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

func (s *Server) handlePostMemory(w http.ResponseWriter, r *http.Request) {
	var entry store.MemoryEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := s.deps.Store.AppendMemory(r.Context(), entry.ScopeID, entry); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.deps.Store.(store.WebStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not supported")
		return
	}

	id, _ := strconv.ParseInt(pathParam(r, "id"), 10, 64)
	scopeID := r.URL.Query().Get("scope")

	if err := ws.DeleteMemory(r.Context(), scopeID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
