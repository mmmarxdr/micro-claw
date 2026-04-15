package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"microagent/internal/config"
	"microagent/internal/store"
)

// fakeMediaStore is a minimal store.MediaStore for testing.
type fakeMediaStore struct{}

func (f *fakeMediaStore) StoreMedia(_ context.Context, _ []byte, _ string) (string, error) {
	return "deadbeef", nil
}

func (f *fakeMediaStore) GetMedia(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", store.ErrMediaNotFound
}

func (f *fakeMediaStore) TouchMedia(_ context.Context, _ string) error { return nil }

func (f *fakeMediaStore) PruneUnreferencedMedia(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeMediaStore) ListMedia(_ context.Context) ([]store.MediaMeta, error) {
	return nil, nil
}

func (f *fakeMediaStore) DeleteMedia(_ context.Context, _ string) error {
	return nil
}

func TestNewServer_NilMediaStore_NoFanic(t *testing.T) {
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
	// Should not panic
	s.routes()
}

func TestServer_MediaStore_NilWhenStoreNil(t *testing.T) {
	enabled := true
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{Enabled: &enabled}

	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: nil,
		},
		mux: http.NewServeMux(),
	}
	if s.mediaStore() != nil {
		t.Error("expected nil when MediaStore is nil")
	}
}

func TestServer_MediaStore_NilWhenMediaDisabled(t *testing.T) {
	disabled := false
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{Enabled: &disabled}

	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: &fakeMediaStore{},
		},
		mux: http.NewServeMux(),
	}
	if s.mediaStore() != nil {
		t.Error("expected nil when Media.Enabled is false")
	}
}

func TestServer_MediaStore_ReturnsStore(t *testing.T) {
	enabled := true
	cfg := minimalConfig()
	cfg.Media = config.MediaConfig{Enabled: &enabled}

	ms := &fakeMediaStore{}
	s := &Server{
		deps: ServerDeps{
			Store:      &noWebStore{},
			Config:     cfg,
			StartedAt:  time.Now(),
			MediaStore: ms,
		},
		mux: http.NewServeMux(),
	}
	got := s.mediaStore()
	if got == nil {
		t.Fatal("expected non-nil MediaStore")
	}
	if got != ms {
		t.Error("expected the same MediaStore instance")
	}
}
