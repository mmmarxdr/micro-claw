package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daimon/internal/config"
	"daimon/internal/store"
)

// testMediaStore is a controllable MediaStore for upload handler tests.
type testMediaStore struct {
	sha       string
	err       error
	getData   []byte
	getMIME   string
	getErr    error
	listData  []store.MediaMeta
	listErr   error
	deleteErr error
	deleteSHA string
}

func (m *testMediaStore) StoreMedia(_ context.Context, _ []byte, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.sha, nil
}

func (m *testMediaStore) GetMedia(_ context.Context, _ string) ([]byte, string, error) {
	if m.getErr != nil {
		return nil, "", m.getErr
	}
	return m.getData, m.getMIME, nil
}

func (m *testMediaStore) TouchMedia(_ context.Context, _ string) error { return nil }

func (m *testMediaStore) PruneUnreferencedMedia(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

func (m *testMediaStore) ListMedia(_ context.Context) ([]store.MediaMeta, error) {
	return m.listData, m.listErr
}

func (m *testMediaStore) DeleteMedia(_ context.Context, sha string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if m.deleteSHA != "" && m.deleteSHA != sha {
		return store.ErrMediaNotFound
	}
	return nil
}

// buildUploadRequest creates a multipart form request with the given file content.
func buildUploadRequest(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if fieldName != "" {
		fw, err := w.CreateFormFile(fieldName, filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		fw.Write(content)
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// newUploadTestServer creates a Server with mediaStore enabled.
func newUploadTestServer(t *testing.T, ms store.MediaStore) *Server {
	t.Helper()
	enabled := true
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{
		Enabled:            &enabled,
		MaxAttachmentBytes: 1 << 20, // 1 MB
		AllowedMIMEPrefixes: []string{"image/", "audio/", "application/pdf", "text/"},
	}
	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: ms,
		},
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// pngHeader is the first 8 bytes of a valid PNG file.
var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

// makePNGContent builds a minimal 512-byte buffer with PNG magic bytes.
func makePNGContent() []byte {
	buf := make([]byte, 512)
	copy(buf, pngHeader)
	return buf
}

func TestHandleUpload_HappyPath(t *testing.T) {
	ms := &testMediaStore{sha: "abc123deadbeef"}
	s := newUploadTestServer(t, ms)

	req := buildUploadRequest(t, "file", "photo.png", makePNGContent())
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["sha256"] != "abc123deadbeef" {
		t.Errorf("sha256 = %v, want abc123deadbeef", resp["sha256"])
	}
	if resp["mime"] == nil || resp["mime"] == "" {
		t.Error("expected non-empty mime in response")
	}
}

func TestHandleUpload_NilMediaStore_Returns503(t *testing.T) {
	// Build a server with no MediaStore but media disabled.
	cfg := minimalConfig()
	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: nil,
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	req := buildUploadRequest(t, "file", "photo.png", makePNGContent())
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandleUpload_Oversized_Returns413(t *testing.T) {
	ms := &testMediaStore{sha: "abc"}
	enabled := true
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{
		Enabled:            &enabled,
		MaxAttachmentBytes: 512, // tiny limit
		AllowedMIMEPrefixes: []string{"image/"},
	}
	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: ms,
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	// Build a 1KB PNG payload (exceeds 512-byte limit).
	bigContent := bytes.Repeat(pngHeader, 200) // ~1600 bytes
	req := buildUploadRequest(t, "file", "big.png", bigContent)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpload_BadMIME_Returns415(t *testing.T) {
	ms := &testMediaStore{sha: "abc"}
	enabled := true
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{
		Enabled:            &enabled,
		MaxAttachmentBytes: 1 << 20,
		AllowedMIMEPrefixes: []string{"image/"},
	}
	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: ms,
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	// Send an executable (non-image) content.
	exeContent := []byte("MZ\x90\x00") // EXE magic bytes
	req := buildUploadRequest(t, "file", "evil.exe", exeContent)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpload_MissingField_Returns400(t *testing.T) {
	ms := &testMediaStore{sha: "abc"}
	s := newUploadTestServer(t, ms)

	// Send a multipart form with wrong field name.
	req := buildUploadRequest(t, "wrong_field", "photo.png", makePNGContent())
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleUpload_ValidImagePng_CorrectResponseJSON(t *testing.T) {
	ms := &testMediaStore{sha: "feedface12345678"}
	s := newUploadTestServer(t, ms)

	req := buildUploadRequest(t, "file", "test.png", makePNGContent())
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		SHA256   string `json:"sha256"`
		MIME     string `json:"mime"`
		Size     int64  `json:"size"`
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SHA256 != "feedface12345678" {
		t.Errorf("sha256 = %q, want feedface12345678", resp.SHA256)
	}
	if resp.MIME != "image/png" {
		t.Errorf("mime = %q, want image/png", resp.MIME)
	}
	if resp.Size != 512 {
		t.Errorf("size = %d, want 512", resp.Size)
	}
	if resp.Filename != "test.png" {
		t.Errorf("filename = %q, want test.png", resp.Filename)
	}
}

// ---------------------------------------------------------------------------
// handleGetMedia tests
// ---------------------------------------------------------------------------

func TestHandleGetMedia_KnownSHA_Returns200(t *testing.T) {
	ms := &testMediaStore{
		getData: []byte("image bytes"),
		getMIME: "image/png",
	}
	s := newUploadTestServer(t, ms)

	sha := fmt.Sprintf("%064x", 0) // 64 hex zeros (valid format)
	req := httptest.NewRequest(http.MethodGet, "/api/media/"+sha, nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if body, _ := io.ReadAll(rr.Body); string(body) != "image bytes" {
		t.Errorf("body = %q, want %q", body, "image bytes")
	}
}

func TestHandleGetMedia_UnknownSHA_Returns404(t *testing.T) {
	ms := &testMediaStore{getErr: store.ErrMediaNotFound}
	s := newUploadTestServer(t, ms)

	sha := fmt.Sprintf("%064x", 1)
	req := httptest.NewRequest(http.MethodGet, "/api/media/"+sha, nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleGetMedia_MalformedSHA_Returns400(t *testing.T) {
	ms := &testMediaStore{}
	s := newUploadTestServer(t, ms)

	req := httptest.NewRequest(http.MethodGet, "/api/media/not-a-sha256", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// handleListMedia tests
// ---------------------------------------------------------------------------

func TestHandleListMedia_ReturnsItems(t *testing.T) {
	ms := &testMediaStore{
		listData: []store.MediaMeta{
			{SHA256: "aaa", MIME: "image/png", Size: 1024, CreatedAt: "2026-04-14T10:00:00Z", LastReferencedAt: "2026-04-14T10:00:00Z"},
			{SHA256: "bbb", MIME: "text/plain", Size: 512, CreatedAt: "2026-04-14T09:00:00Z", LastReferencedAt: "2026-04-14T09:00:00Z"},
		},
	}
	s := newUploadTestServer(t, ms)

	req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var items []store.MediaMeta
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].SHA256 != "aaa" {
		t.Errorf("first item sha256 = %q, want aaa", items[0].SHA256)
	}
}

func TestHandleListMedia_EmptyReturnsEmptyArray(t *testing.T) {
	ms := &testMediaStore{}
	s := newUploadTestServer(t, ms)

	req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestHandleListMedia_DisabledStore_Returns503(t *testing.T) {
	cfg := minimalConfig()
	s := &Server{
		deps: ServerDeps{
			Store:     &noWebStore{},
			Config:    cfg,
			StartedAt: time.Now(),
		},
		mux: http.NewServeMux(),
	}
	s.routes()

	req := httptest.NewRequest(http.MethodGet, "/api/media", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// handleDeleteMedia tests
// ---------------------------------------------------------------------------

func TestHandleDeleteMedia_KnownSHA_Returns204(t *testing.T) {
	sha := fmt.Sprintf("%064x", 42)
	ms := &testMediaStore{deleteSHA: sha}
	s := newUploadTestServer(t, ms)

	req := httptest.NewRequest(http.MethodDelete, "/api/media/"+sha, nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDeleteMedia_UnknownSHA_Returns404(t *testing.T) {
	ms := &testMediaStore{deleteErr: store.ErrMediaNotFound}
	s := newUploadTestServer(t, ms)

	sha := fmt.Sprintf("%064x", 99)
	req := httptest.NewRequest(http.MethodDelete, "/api/media/"+sha, nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleDeleteMedia_MalformedSHA_Returns400(t *testing.T) {
	ms := &testMediaStore{}
	s := newUploadTestServer(t, ms)

	req := httptest.NewRequest(http.MethodDelete, "/api/media/not-valid", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}
