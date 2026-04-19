package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"daimon/internal/store"
)

// memoryEntryResponse is the API shape the frontend expects for a MemoryEntry.
type memoryEntryResponse struct {
	ID                   string   `json:"id"`
	Content              string   `json:"content"`
	Tags                 []string `json:"tags"`
	SourceConversationID string   `json:"source_conversation_id"`
	CreatedAt            string   `json:"created_at"`
}

func toMemoryEntryResponse(e store.MemoryEntry) memoryEntryResponse {
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return memoryEntryResponse{
		ID:                   e.ID,
		Content:              e.Content,
		Tags:                 tags,
		SourceConversationID: e.Source,
		CreatedAt:            e.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

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

	items := make([]memoryEntryResponse, 0, len(entries))
	for _, e := range entries {
		items = append(items, toMemoryEntryResponse(e))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePostMemory(w http.ResponseWriter, r *http.Request) {
	var entry store.MemoryEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if entry.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if entry.ScopeID == "" {
		writeError(w, http.StatusBadRequest, "scope_id is required")
		return
	}

	// Assign a server-generated ID — never trust caller-supplied IDs.
	entry.ID = uuid.New().String()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	if err := s.deps.Store.AppendMemory(r.Context(), entry.ScopeID, entry); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toMemoryEntryResponse(entry))
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.deps.Store.(store.WebStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not supported")
		return
	}

	rawID := pathParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid memory id: must be a number")
		return
	}
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
