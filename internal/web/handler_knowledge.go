package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"daimon/internal/rag"
	"daimon/internal/store"
)

// knowledgeDocResponse is the API shape for a single ingested document, as
// consumed by the Memory → Knowledge tab. Mirrors the design fixture
// `KnowledgeDoc` in daimon-frontend/src/design/memoryMocks.ts.
type knowledgeDocResponse struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	MIME           string  `json:"mime,omitempty"`
	KindHint       string  `json:"kind_hint,omitempty"` // pdf | markdown | docx | html | zip | plain
	SHA256         string  `json:"sha256,omitempty"`
	Size           int64   `json:"size,omitempty"`
	ChunkCount     int     `json:"chunk_count"`
	TokenCount     int     `json:"token_count"`
	PageCount      *int    `json:"page_count,omitempty"`
	AccessCount    int     `json:"access_count"`
	LastAccessedAt string  `json:"last_accessed_at,omitempty"`
	Summary        string  `json:"summary,omitempty"`
	Status         string  `json:"status"` // ready | indexing
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// mimeToKindHint derives the frontend `KnowledgeType` bucket from a MIME. Unknown
// MIMEs fall back to `plain` — the frontend renders that as a plain-text glyph.
func mimeToKindHint(mime string) string {
	lower := strings.ToLower(strings.TrimSpace(mime))
	switch {
	case lower == "application/pdf":
		return "pdf"
	case strings.HasPrefix(lower, "text/markdown"), strings.HasSuffix(lower, "+markdown"):
		return "markdown"
	case lower == "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		lower == "application/msword":
		return "docx"
	case lower == "text/html":
		return "html"
	case lower == "application/zip", lower == "application/x-zip-compressed":
		return "zip"
	default:
		return "plain"
	}
}

// statusFromDoc derives the user-facing status from the document state:
//   - "ready":   chunks > 0 (worker produced searchable content)
//   - "empty":   worker finished (IngestedAt set) but produced 0 chunks
//   - "indexing": worker has not yet stamped IngestedAt
//
// "empty" prevents the UI from hanging on PDFs the extractor cannot read.
func statusFromDoc(d rag.Document) string {
	if d.ChunkCount > 0 {
		return "ready"
	}
	if d.IngestedAt != nil && !d.IngestedAt.IsZero() {
		return "empty"
	}
	return "indexing"
}

func formatTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// handleListKnowledge serves GET /api/knowledge. Returns `{items: [...]}`.
// Enriches each Document with:
//   - size pulled from the MediaStore by source_sha256 (when present).
//   - token_count aggregated from document_chunks (sum per doc_id).
//   - status derived from chunk_count (0 → indexing, otherwise ready).
func (s *Server) handleListKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DocStore == nil {
		writeError(w, http.StatusNotImplemented, "knowledge base is not configured (RAG disabled)")
		return
	}
	docs, err := s.deps.DocStore.ListDocuments(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Aggregate tokens per doc where possible (SQLite backend only).
	tokensByDoc := map[string]int{}
	if sqlDocs, ok := s.deps.DocStore.(*rag.SQLiteDocumentStore); ok && len(docs) > 0 {
		ids := make([]string, 0, len(docs))
		for _, d := range docs {
			ids = append(ids, d.ID)
		}
		if sums, err := sqlDocs.SumTokensByDoc(r.Context(), ids); err == nil {
			tokensByDoc = sums
		}
	}

	// Batch media meta lookups — cache by SHA to avoid N queries when multiple
	// docs share a blob (rare but cheap to guard against).
	mediaCache := map[string]store.MediaMeta{}

	items := make([]knowledgeDocResponse, 0, len(docs))
	for _, d := range docs {
		item := knowledgeDocResponse{
			ID:             d.ID,
			Title:          d.Title,
			MIME:           d.MIME,
			KindHint:       mimeToKindHint(d.MIME),
			SHA256:         d.SourceSHA256,
			ChunkCount:     d.ChunkCount,
			TokenCount:     tokensByDoc[d.ID],
			PageCount:      d.PageCount,
			AccessCount:    d.AccessCount,
			LastAccessedAt: formatTimePtr(d.LastAccessedAt),
			Summary:        d.Summary,
			Status:         statusFromDoc(d),
			CreatedAt:      d.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:      d.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if d.SourceSHA256 != "" && s.deps.MediaStore != nil {
			meta, ok := mediaCache[d.SourceSHA256]
			if !ok {
				if all, err := s.deps.MediaStore.ListMedia(r.Context()); err == nil {
					for _, m := range all {
						mediaCache[m.SHA256] = m
					}
					meta = mediaCache[d.SourceSHA256]
				}
			}
			item.Size = meta.Size
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handlePostKnowledge serves POST /api/knowledge (multipart/form-data `file`).
// Stores the blob, creates a placeholder document row (chunk_count=0 so the
// frontend shows "indexing"), and enqueues the ingestion job. Returns the
// placeholder shape immediately — the worker updates chunk_count + embeddings
// asynchronously.
func (s *Server) handlePostKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DocStore == nil || s.deps.IngestWorker == nil {
		writeError(w, http.StatusNotImplemented, "knowledge base is not configured (RAG disabled)")
		return
	}
	ms := s.mediaStore()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "media uploads are disabled")
		return
	}

	maxBytes := s.config().Media.MaxAttachmentBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	if err := r.ParseMultipartForm(maxBytes); err != nil {
		if strings.Contains(err.Error(), "too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart form")
		}
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	sniff := make([]byte, 512)
	n, _ := file.Read(sniff)
	sniff = sniff[:n]
	detectedMIME := http.DetectContentType(sniff)
	if idx := strings.Index(detectedMIME, ";"); idx >= 0 {
		detectedMIME = strings.TrimSpace(detectedMIME[:idx])
	}

	// Read the rest of the body.
	rest := make([]byte, maxBytes)
	m, _ := file.Read(rest)
	data := append(sniff, rest[:m]...)

	sha, err := ms.StoreMedia(r.Context(), data, detectedMIME)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store media")
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = header.Filename
	}
	if title == "" {
		title = "untitled"
	}

	docID := uuid.New().String()
	now := time.Now().UTC()
	doc := rag.Document{
		ID:           docID,
		Namespace:    "global",
		Title:        title,
		SourceSHA256: sha,
		MIME:         detectedMIME,
		ChunkCount:   0,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.deps.DocStore.AddDocument(r.Context(), doc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.deps.IngestWorker.Enqueue(rag.IngestionJob{
		DocID:     docID,
		Namespace: "global",
		Title:     title,
		SHA256:    sha,
		MIME:      detectedMIME,
	})

	writeJSON(w, http.StatusCreated, knowledgeDocResponse{
		ID:         docID,
		Title:      title,
		MIME:       detectedMIME,
		KindHint:   mimeToKindHint(detectedMIME),
		SHA256:     sha,
		Size:       int64(len(data)),
		ChunkCount: 0,
		Status:     "indexing",
		CreatedAt:  now.Format(time.RFC3339),
		UpdatedAt:  now.Format(time.RFC3339),
	})
}

// handleDeleteKnowledge serves DELETE /api/knowledge/{id}. Chunks cascade via
// the FK. The source media blob is left in place — it may be referenced by
// other documents and is GC'd by the media retention job.
func (s *Server) handleDeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	if s.deps.DocStore == nil {
		writeError(w, http.StatusNotImplemented, "knowledge base is not configured (RAG disabled)")
		return
	}
	id := pathParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing document id")
		return
	}
	if err := s.deps.DocStore.DeleteDocument(r.Context(), id); err != nil {
		if errors.Is(err, rag.ErrDocNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
