package web

import (
	"errors"
	"net/http"
	"strconv"

	"daimon/internal/store"
)

// apiMessage is the wire shape for a single conversation message sent to the frontend.
type apiMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// apiConversation is the wire shape for a full conversation sent to the frontend.
type apiConversation struct {
	ID        string       `json:"id"`
	ChannelID string       `json:"channel_id"`
	Messages  []apiMessage `json:"messages"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
}

func toAPIConversation(c *store.Conversation) apiConversation {
	msgs := make([]apiMessage, 0, len(c.Messages))
	for _, m := range c.Messages {
		msgs = append(msgs, apiMessage{
			Role:    m.Role,
			Content: m.Content.TextOnly(),
		})
	}
	return apiConversation{
		ID:        c.ID,
		ChannelID: c.ChannelID,
		Messages:  msgs,
		CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	channel := r.URL.Query().Get("channel")

	ws, ok := s.deps.Store.(store.WebStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "conversation listing not supported by current store backend")
		return
	}

	convs, total, err := ws.ListConversationsPaginated(r.Context(), channel, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type convSummary struct {
		ID           string `json:"id"`
		ChannelID    string `json:"channel_id"`
		MessageCount int    `json:"message_count"`
		LastMessage  string `json:"last_message,omitempty"`
		UpdatedAt    string `json:"updated_at,omitempty"`
	}

	items := make([]convSummary, 0, len(convs))
	for _, c := range convs {
		summary := convSummary{
			ID:           c.ID,
			ChannelID:    c.ChannelID,
			MessageCount: len(c.Messages),
			UpdatedAt:    c.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if len(c.Messages) > 0 {
			last := c.Messages[len(c.Messages)-1]
			summary.LastMessage = truncate(last.Content.TextOnly(), 100)
		}
		items = append(items, summary)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")

	conv, err := s.deps.Store.LoadConversation(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "conversation not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, toAPIConversation(conv))
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.deps.Store.(store.WebStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not supported")
		return
	}

	id := pathParam(r, "id")
	if err := ws.DeleteConversation(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}

		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// truncate shortens s to at most n runes, appending "…" if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}

	return string(runes[:n]) + "…"
}
