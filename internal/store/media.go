package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrMediaNotFound is returned by GetMedia and TouchMedia when the sha256
	// does not exist in the media_blobs table.
	ErrMediaNotFound = errors.New("media blob not found")

	// ErrMediaNotSupported is returned by any MediaStore method on a backend
	// that does not support binary media storage (e.g. FileStore).
	// Callers should type-assert store to MediaStore before using media ops:
	//
	//	if ms, ok := store.(store.MediaStore); ok { ... } else { log.Warn("...") }
	ErrMediaNotSupported = errors.New("media store not supported by this backend")
)

// MediaStore is an optional extension of Store for content-addressable binary
// media blobs (images, audio, documents). Only SQLiteStore implements this;
// FileStore does NOT — callers must type-assert:
//
//	ms, ok := myStore.(store.MediaStore)
//
// *SQLiteStore satisfies the provider-internal mediaReader interface
// (GetMedia signature identical) via Go structural typing — no import cycle.
//
// main.go logs a startup warning when cfg.Media.Enabled=true and the backing
// store does not implement MediaStore (e.g. FileStore).
type MediaStore interface {
	// StoreMedia content-addressably stores data with the given MIME type.
	// Returns the lowercase hex SHA-256 digest. If the blob already exists
	// (same sha256) the call is a no-op and the same digest is returned.
	StoreMedia(ctx context.Context, data []byte, mime string) (sha256 string, err error)

	// GetMedia retrieves blob bytes and MIME type by SHA-256 hex digest.
	// Returns ErrMediaNotFound if the digest is unknown.
	GetMedia(ctx context.Context, sha256 string) (data []byte, mime string, err error)

	// TouchMedia updates last_referenced_at to now for the given sha256.
	// Returns ErrMediaNotFound if the digest is unknown.
	TouchMedia(ctx context.Context, sha256 string) error

	// PruneUnreferencedMedia deletes blobs whose last_referenced_at is older
	// than olderThan and that are not referenced by any stored conversation.
	// Returns the number of blobs deleted.
	PruneUnreferencedMedia(ctx context.Context, olderThan time.Duration) (deleted int, err error)

	// ListMedia returns metadata for all stored blobs, ordered by creation
	// time descending (newest first). The blob data itself is NOT included.
	ListMedia(ctx context.Context) ([]MediaMeta, error)

	// DeleteMedia removes a blob by its SHA-256 hex digest.
	// Returns ErrMediaNotFound if the digest is unknown.
	DeleteMedia(ctx context.Context, sha256 string) error
}

// MediaMeta holds metadata about a stored media blob (without the data).
type MediaMeta struct {
	SHA256           string `json:"sha256"`
	MIME             string `json:"mime"`
	Size             int64  `json:"size"`
	CreatedAt        string `json:"created_at"`
	LastReferencedAt string `json:"last_referenced_at"`
}
