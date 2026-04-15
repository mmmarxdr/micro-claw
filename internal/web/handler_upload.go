package web

import (
	"encoding/hex"
	"net/http"
	"strings"

	"microagent/internal/store"
)

// uploadResponse is the JSON response body for a successful upload.
type uploadResponse struct {
	SHA256   string `json:"sha256"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Filename string `json:"filename,omitempty"`
}

// handleUpload handles POST /api/upload — stores a multipart-uploaded file in
// the MediaStore and returns its content-addressable SHA-256 digest.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	ms := s.mediaStore()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "media uploads are disabled")
		return
	}

	// Override body limit for this handler.
	maxBytes := s.deps.Config.Media.MaxAttachmentBytes
	if maxBytes <= 0 {
		maxBytes = 10 << 20 // 10 MB default
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	// Parse multipart form.
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		if strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "request body too large") {
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

	// Read up to 512 bytes for MIME detection.
	sniff := make([]byte, 512)
	n, _ := file.Read(sniff)
	sniff = sniff[:n]
	detectedMIME := http.DetectContentType(sniff)
	// Normalize: strip parameters like "; charset=utf-8".
	if idx := strings.Index(detectedMIME, ";"); idx >= 0 {
		detectedMIME = strings.TrimSpace(detectedMIME[:idx])
	}

	// Check against allowed MIME prefixes.
	if !mimeAllowed(detectedMIME, s.deps.Config.Media.AllowedMIMEPrefixes) {
		writeError(w, http.StatusUnsupportedMediaType, "file type not allowed")
		return
	}

	// Read the rest of the file after the sniff bytes.
	// We need the full data for StoreMedia — rebuild from sniff + remainder.
	// Use a limited reader to enforce the size cap on the rest.
	limitedRemainder := http.MaxBytesReader(w, file, maxBytes)
	var buf []byte
	buf = append(buf, sniff...)
	rest := make([]byte, maxBytes)
	m, readErr := limitedRemainder.Read(rest)
	if readErr != nil && readErr.Error() != "EOF" && !strings.Contains(readErr.Error(), "http: request body too large") {
		buf = append(buf, rest[:m]...)
	} else {
		buf = append(buf, rest[:m]...)
	}
	data := buf

	// Store the media blob.
	sha, err := ms.StoreMedia(r.Context(), data, detectedMIME)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store media")
		return
	}

	writeJSON(w, http.StatusCreated, uploadResponse{
		SHA256:   sha,
		MIME:     detectedMIME,
		Size:     int64(len(data)),
		Filename: header.Filename,
	})
}

// handleGetMedia handles GET /api/media/{sha256} — retrieves a stored blob by
// its SHA-256 hex digest.
func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	ms := s.mediaStore()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "media uploads are disabled")
		return
	}

	sha := pathParam(r, "sha256")

	// Validate: must be exactly 64 lowercase hex characters.
	if len(sha) != 64 {
		writeError(w, http.StatusBadRequest, "invalid sha256: must be 64 hex characters")
		return
	}
	if _, err := hex.DecodeString(sha); err != nil {
		writeError(w, http.StatusBadRequest, "invalid sha256: not valid hex")
		return
	}

	data, mime, err := ms.GetMedia(r.Context(), sha)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// handleListMedia handles GET /api/media — returns metadata for all stored blobs.
func (s *Server) handleListMedia(w http.ResponseWriter, r *http.Request) {
	ms := s.mediaStore()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "media uploads are disabled")
		return
	}

	items, err := ms.ListMedia(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list media")
		return
	}

	if items == nil {
		items = []store.MediaMeta{}
	}

	writeJSON(w, http.StatusOK, items)
}

// handleDeleteMedia handles DELETE /api/media/{sha256} — removes a stored blob.
func (s *Server) handleDeleteMedia(w http.ResponseWriter, r *http.Request) {
	ms := s.mediaStore()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "media uploads are disabled")
		return
	}

	sha := pathParam(r, "sha256")

	if len(sha) != 64 {
		writeError(w, http.StatusBadRequest, "invalid sha256: must be 64 hex characters")
		return
	}
	if _, err := hex.DecodeString(sha); err != nil {
		writeError(w, http.StatusBadRequest, "invalid sha256: not valid hex")
		return
	}

	if err := ms.DeleteMedia(r.Context(), sha); err != nil {
		if err == store.ErrMediaNotFound {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete media")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// mimeAllowed reports whether mime matches any of the allowed prefix patterns.
func mimeAllowed(mime string, allowed []string) bool {
	for _, prefix := range allowed {
		if strings.HasPrefix(mime, prefix) {
			return true
		}
	}
	return false
}
