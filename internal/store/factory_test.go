package store

import (
	"strings"
	"testing"

	"microagent/internal/config"
)

func TestNew_FileType(t *testing.T) {
	s, err := New(config.StoreConfig{Type: "file", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("New(file): %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store for type 'file'")
	}
	defer s.Close()
}

func TestNew_EmptyType(t *testing.T) {
	s, err := New(config.StoreConfig{Type: "", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("New(empty): %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store for empty type (defaults to FileStore)")
	}
	defer s.Close()
}

func TestNew_SQLiteType(t *testing.T) {
	s, err := New(config.StoreConfig{Type: "sqlite", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil store for type 'sqlite'")
	}
	defer s.Close()
}

func TestNew_UnknownType(t *testing.T) {
	s, err := New(config.StoreConfig{Type: "badger"})
	if err == nil {
		if s != nil {
			s.Close()
		}
		t.Fatal("expected error for unknown type 'badger', got nil")
	}
	if s != nil {
		s.Close()
		t.Error("expected nil store on error, got non-nil")
	}
	if !strings.Contains(err.Error(), "unknown store type") {
		t.Errorf("error should mention 'unknown store type', got: %v", err)
	}
}
