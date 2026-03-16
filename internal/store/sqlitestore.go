package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"microagent/internal/config"

	_ "modernc.org/sqlite" // register "sqlite" driver with database/sql
)

const schema = `
CREATE TABLE IF NOT EXISTS conversations (
	id         TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	messages   TEXT NOT NULL,
	metadata   TEXT,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conv_channel
	ON conversations(channel_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS memory (
	id         TEXT PRIMARY KEY,
	scope_id   TEXT NOT NULL,
	topic      TEXT,
	type       TEXT,
	title      TEXT,
	content    TEXT NOT NULL,
	tags       TEXT,
	source     TEXT,
	created_at DATETIME NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
	content,
	tags,
	content='memory',
	content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS memory_ai
	AFTER INSERT ON memory BEGIN
		INSERT INTO memory_fts(rowid, content, tags)
		VALUES (new.rowid, new.content, new.tags);
	END;

CREATE TRIGGER IF NOT EXISTS memory_ad
	AFTER DELETE ON memory BEGIN
		INSERT INTO memory_fts(memory_fts, rowid, content, tags)
		VALUES ('delete', old.rowid, old.content, old.tags);
	END;

CREATE TABLE IF NOT EXISTS secrets (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,
	updated_at DATETIME NOT NULL
);
`

// SQLiteStore is a Store implementation backed by a SQLite database.
// Open via NewSQLiteStore; close via Close when done.
type SQLiteStore struct {
	db        *sql.DB
	cfg       config.StoreConfig
	closeOnce sync.Once
}

// NewSQLiteStore opens (or creates) a SQLite database at cfg.Path/microagent.db,
// enables WAL mode, and applies the schema. Returns an error if the database
// cannot be opened or the schema cannot be applied.
func NewSQLiteStore(cfg config.StoreConfig) (*SQLiteStore, error) {
	if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
		return nil, fmt.Errorf("creating store directory %s: %w", cfg.Path, err)
	}

	dbPath := filepath.Join(cfg.Path, "microagent.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %s: %w", dbPath, err)
	}

	// Enable WAL mode for concurrent reads alongside writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	s := &SQLiteStore{db: db, cfg: cfg}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return s, nil
}

// initSchema executes all CREATE TABLE / CREATE VIRTUAL TABLE / CREATE INDEX /
// CREATE TRIGGER statements. Called once during NewSQLiteStore.
func (s *SQLiteStore) initSchema() error {
	_, err := s.db.Exec(schema)
	return err
}

// Close releases database resources. Safe to call multiple times.
func (s *SQLiteStore) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.db.Close()
	})
	return closeErr
}

// SaveConversation persists a conversation, overwriting any existing entry with the same ID.
func (s *SQLiteStore) SaveConversation(ctx context.Context, conv Conversation) error {
	messages, err := json.Marshal(conv.Messages)
	if err != nil {
		return fmt.Errorf("marshalling messages: %w", err)
	}

	metadata, err := json.Marshal(conv.Metadata)
	if err != nil {
		return fmt.Errorf("marshalling metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO conversations (id, channel_id, messages, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		conv.ID, conv.ChannelID, string(messages), string(metadata),
		conv.CreatedAt, conv.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("saving conversation %s: %w", conv.ID, err)
	}
	return nil
}

// LoadConversation retrieves a conversation by ID. Returns ErrNotFound (wrapped) if not found.
func (s *SQLiteStore) LoadConversation(ctx context.Context, id string) (*Conversation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, channel_id, messages, metadata, created_at, updated_at
		 FROM conversations WHERE id = ?`, id)

	var conv Conversation
	var messagesJSON, metadataJSON string

	err := row.Scan(
		&conv.ID, &conv.ChannelID, &messagesJSON, &metadataJSON,
		&conv.CreatedAt, &conv.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("loading conversation %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("loading conversation %s: %w", id, err)
	}

	if err := json.Unmarshal([]byte(messagesJSON), &conv.Messages); err != nil {
		return nil, fmt.Errorf("unmarshalling messages for conversation %s: %w", id, err)
	}
	if metadataJSON != "" && metadataJSON != "null" {
		if err := json.Unmarshal([]byte(metadataJSON), &conv.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshalling metadata for conversation %s: %w", id, err)
		}
	}

	return &conv, nil
}

// ListConversations returns conversations filtered by channelID (or all if empty),
// ordered by UpdatedAt descending, limited to limit results (0 = no limit).
func (s *SQLiteStore) ListConversations(ctx context.Context, channelID string, limit int) ([]Conversation, error) {
	var query string
	var args []any

	if channelID != "" {
		query = `SELECT id, channel_id, messages, metadata, created_at, updated_at
		          FROM conversations WHERE channel_id = ? ORDER BY updated_at DESC`
		args = append(args, channelID)
	} else {
		query = `SELECT id, channel_id, messages, metadata, created_at, updated_at
		          FROM conversations ORDER BY updated_at DESC`
	}

	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing conversations: %w", err)
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		var conv Conversation
		var messagesJSON, metadataJSON string

		if err := rows.Scan(
			&conv.ID, &conv.ChannelID, &messagesJSON, &metadataJSON,
			&conv.CreatedAt, &conv.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning conversation row: %w", err)
		}

		if err := json.Unmarshal([]byte(messagesJSON), &conv.Messages); err != nil {
			return nil, fmt.Errorf("unmarshalling messages: %w", err)
		}
		if metadataJSON != "" && metadataJSON != "null" {
			if err := json.Unmarshal([]byte(metadataJSON), &conv.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshalling metadata: %w", err)
			}
		}

		convs = append(convs, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating conversation rows: %w", err)
	}

	if convs == nil {
		convs = []Conversation{}
	}
	return convs, nil
}

// AppendMemory adds a new memory entry under the given scopeID. The FTS5 trigger fires automatically.
func (s *SQLiteStore) AppendMemory(ctx context.Context, scopeID string, entry MemoryEntry) error {
	tagsJSON, err := json.Marshal(entry.Tags)
	if err != nil {
		return fmt.Errorf("marshalling tags: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO memory (id, scope_id, topic, type, title, content, tags, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, scopeID, entry.Topic, entry.Type, entry.Title,
		entry.Content, string(tagsJSON), entry.Source, entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("appending memory entry %s: %w", entry.ID, err)
	}
	return nil
}

// SearchMemory searches memory entries in scopeID matching query (FTS5 if non-empty, plain SELECT if empty).
// limit <= 0 means no limit.
func (s *SQLiteStore) SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error) {
	var rows *sql.Rows
	var err error

	if query == "" {
		// Empty query: return all entries for scope ordered by created_at DESC.
		q := `SELECT id, scope_id, topic, type, title, content, tags, source, created_at
		      FROM memory WHERE scope_id = ? ORDER BY created_at DESC`
		var args []any
		args = append(args, scopeID)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	} else {
		// Non-empty query: use FTS5 join ordered by relevance rank.
		q := `SELECT m.id, m.scope_id, m.topic, m.type, m.title, m.content, m.tags, m.source, m.created_at
		      FROM memory m JOIN memory_fts f ON f.rowid = m.rowid
		      WHERE f.memory_fts MATCH ? AND m.scope_id = ? ORDER BY rank`
		var args []any
		args = append(args, query, scopeID)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("searching memory in scope %s: %w", scopeID, err)
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var entry MemoryEntry
		var tagsJSON string

		if err := rows.Scan(
			&entry.ID, &entry.ScopeID, &entry.Topic, &entry.Type, &entry.Title,
			&entry.Content, &tagsJSON, &entry.Source, &entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning memory row: %w", err)
		}

		if tagsJSON != "" && tagsJSON != "null" {
			if err := json.Unmarshal([]byte(tagsJSON), &entry.Tags); err != nil {
				return nil, fmt.Errorf("unmarshalling tags: %w", err)
			}
		}

		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating memory rows: %w", err)
	}

	if entries == nil {
		entries = []MemoryEntry{}
	}
	return entries, nil
}

// Compile-time assertion: SQLiteStore must satisfy SecretsStore.
var _ SecretsStore = (*SQLiteStore)(nil)

// encryptionKey resolves the AES-256 encryption key from config or environment.
// Resolution order: StoreConfig.EncryptionKey → MICROAGENT_SECRET_KEY env var.
// Returns ErrEncryptionKeyNotConfigured if neither is set.
func (s *SQLiteStore) encryptionKey() ([]byte, error) {
	hexKey := s.cfg.EncryptionKey
	if hexKey == "" {
		hexKey = os.Getenv("MICROAGENT_SECRET_KEY")
	}
	if hexKey == "" {
		return nil, ErrEncryptionKeyNotConfigured
	}
	return parseEncryptionKey(hexKey)
}

// SetSecret encrypts value and persists it under key (upsert semantics).
// Returns ErrEncryptionKeyNotConfigured if no encryption key is configured.
// Returns an error if key is empty.
func (s *SQLiteStore) SetSecret(ctx context.Context, key string, value string) error {
	if key == "" {
		return fmt.Errorf("secret key must not be empty")
	}
	aesKey, err := s.encryptionKey()
	if err != nil {
		return err
	}
	encrypted, err := encrypt(aesKey, value)
	if err != nil {
		return fmt.Errorf("encrypting secret %q: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO secrets (key, value, updated_at) VALUES (?, ?, ?)`,
		key, encrypted, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("persisting secret %q: %w", key, err)
	}
	return nil
}

// GetSecret retrieves and decrypts the secret for key.
// Returns ErrNotFound (wrapped) if key does not exist.
// Returns ErrEncryptionKeyNotConfigured if no encryption key is configured.
func (s *SQLiteStore) GetSecret(ctx context.Context, key string) (string, error) {
	aesKey, err := s.encryptionKey()
	if err != nil {
		return "", err
	}
	var encrypted string
	err = s.db.QueryRowContext(ctx,
		`SELECT value FROM secrets WHERE key = ?`, key,
	).Scan(&encrypted)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("getting secret %q: %w", key, ErrNotFound)
		}
		return "", fmt.Errorf("querying secret %q: %w", key, err)
	}
	plaintext, err := decrypt(aesKey, encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypting secret %q: %w", key, err)
	}
	return plaintext, nil
}

// DeleteSecret removes the secret for key. Idempotent — no error if key is absent.
func (s *SQLiteStore) DeleteSecret(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("deleting secret %q: %w", key, err)
	}
	return nil
}

// ListSecretKeys returns all stored secret key names. Values are never returned.
// Returns an empty non-nil slice if no secrets exist.
func (s *SQLiteStore) ListSecretKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM secrets ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("listing secret keys: %w", err)
	}
	defer rows.Close()

	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scanning secret key row: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating secret key rows: %w", err)
	}
	return keys, nil
}
