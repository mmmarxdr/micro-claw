package store

import (
	"context"
	"errors"
	"time"

	"microagent/internal/provider"
)

// ErrNotFound is returned when a requested conversation does not exist.
var ErrNotFound = errors.New("not found")

// ErrEncryptionKeyNotConfigured is returned when a SecretsStore method is called
// but no encryption key has been configured via store.encryption_key or MICROAGENT_SECRET_KEY.
var ErrEncryptionKeyNotConfigured = errors.New("encryption key not configured")

type Conversation struct {
	ID        string                 `json:"id"`
	ChannelID string                 `json:"channel_id"`
	Messages  []provider.ChatMessage `json:"messages"`
	Metadata  map[string]string      `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type MemoryEntry struct {
	ID        string    `json:"id"`
	ScopeID   string    `json:"scope_id"`
	Topic     string    `json:"topic,omitempty"`
	Type      string    `json:"type,omitempty"`
	Title     string    `json:"title,omitempty"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source"` // conversation ID
	CreatedAt time.Time `json:"created_at"`
}

type Store interface {
	SaveConversation(ctx context.Context, conv Conversation) error
	LoadConversation(ctx context.Context, id string) (*Conversation, error)
	ListConversations(ctx context.Context, channelID string, limit int) ([]Conversation, error)
	AppendMemory(ctx context.Context, scopeID string, entry MemoryEntry) error
	SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error)
	Close() error
}

// SecretsStore is an optional extension of Store for encrypted key-value secrets.
// Only SQLiteStore implements this interface. Callers type-assert:
//
//	ss, ok := myStore.(store.SecretsStore)
type SecretsStore interface {
	// GetSecret retrieves and decrypts the secret for key.
	// Returns ErrNotFound (wrapped) if key does not exist.
	// Returns ErrEncryptionKeyNotConfigured if no key is configured.
	GetSecret(ctx context.Context, key string) (string, error)

	// SetSecret encrypts and persists value under key (upsert semantics).
	// Returns ErrEncryptionKeyNotConfigured if no key is configured.
	// Returns an error if key is empty.
	SetSecret(ctx context.Context, key string, value string) error

	// DeleteSecret removes the secret for key. Idempotent — no error if key is absent.
	DeleteSecret(ctx context.Context, key string) error

	// ListSecretKeys returns all stored secret key names (never values).
	// Returns an empty non-nil slice if no secrets exist.
	ListSecretKeys(ctx context.Context) ([]string, error)
}
