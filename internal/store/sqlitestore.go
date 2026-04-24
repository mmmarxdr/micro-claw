package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"daimon/internal/config"
	"daimon/internal/provider"

	_ "modernc.org/sqlite" // register "sqlite" driver with database/sql
)

// SQLiteStore is a Store implementation backed by a SQLite database.
// Open via NewSQLiteStore; close via Close when done.
type SQLiteStore struct {
	db             *sql.DB
	cfg            config.StoreConfig
	closeOnce      sync.Once
	embedQueryFunc func(ctx context.Context, text string) ([]float32, error) // nil when disabled
}

// NewSQLiteStore opens (or creates) a SQLite database at cfg.Path/daimon.db,
// enables WAL mode, and applies the schema. Returns an error if the database
// cannot be opened or the schema cannot be applied.
func NewSQLiteStore(cfg config.StoreConfig) (*SQLiteStore, error) {
	if err := os.MkdirAll(cfg.Path, 0o755); err != nil {
		return nil, fmt.Errorf("creating store directory %s: %w", cfg.Path, err)
	}

	dbPath := filepath.Join(cfg.Path, "daimon.db")
	// Embed pragmas in the DSN so every connection in the pool inherits them —
	// a PRAGMA set via db.Exec only applies to the one connection that ran it.
	// busy_timeout: wait up to 5 s on a locked write (fixes SQLITE_BUSY from
	// the async IndexingWorker racing against the main agent loop).
	// journal_mode=WAL: allows concurrent readers alongside a writer.
	// foreign_keys: enforce ON DELETE CASCADE on document_chunks (and any future
	// FK we add). SQLite defaults this OFF — without it, deleting a document
	// leaves its chunks orphaned and search returns ghost results.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %s: %w", dbPath, err)
	}

	s := &SQLiteStore{db: db, cfg: cfg}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return s, nil
}

// initSchema creates tables if they don't exist and runs versioned migrations.
// Called once during NewSQLiteStore. Idempotent — safe to call multiple times.
func (s *SQLiteStore) initSchema() error {
	return s.initSchemaVersioned()
}

// Close releases database resources. Safe to call multiple times.
func (s *SQLiteStore) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.db.Close()
	})
	return closeErr
}

// DB returns the underlying *sql.DB handle. Used by EmbeddingWorker for direct
// UPDATE queries without routing through the Store interface.
// NOT part of the Store interface — callers must type-assert to *SQLiteStore.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// SetEmbedQueryFunc registers a function that generates a query embedding for
// two-phase semantic search reranking. When set, SearchMemory will call this
// function on the search query text and use cosine similarity to rerank FTS5
// candidates (when ≥2 have stored embeddings). Pass nil to disable reranking.
func (s *SQLiteStore) SetEmbedQueryFunc(fn func(ctx context.Context, text string) ([]float32, error)) {
	s.embedQueryFunc = fn
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

	// Best-effort: keep referenced media blobs alive so the GC prune does not
	// collect them. Errors are ignored — a failed touch only risks early GC.
	if shas := collectMediaSHAs(conv.Messages); len(shas) > 0 {
		_ = s.touchMediaBatch(ctx, shas)
	}

	return nil
}

// LoadConversation retrieves a conversation by ID. Returns ErrNotFound (wrapped) if
// not found or if the conv is soft-deleted (deleted_at IS NOT NULL).
func (s *SQLiteStore) LoadConversation(ctx context.Context, id string) (*Conversation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, channel_id, messages, metadata, created_at, updated_at
		 FROM conversations WHERE id = ? AND deleted_at IS NULL`, id)

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

	// Best-effort: refresh last_referenced_at for any media blobs in this
	// conversation so the GC prune does not collect recently-read blobs.
	if shas := collectMediaSHAs(conv.Messages); len(shas) > 0 {
		_ = s.touchMediaBatch(ctx, shas)
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
		          FROM conversations WHERE channel_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC`
		args = append(args, channelID)
	} else {
		query = `SELECT id, channel_id, messages, metadata, created_at, updated_at
		          FROM conversations WHERE deleted_at IS NULL ORDER BY updated_at DESC`
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

// ListConversationsPaginated returns conversations filtered by channelID prefix
// (or all if empty), ordered by updated_at descending, with pagination support.
// Returns the page slice, total count, and any error.
func (s *SQLiteStore) ListConversationsPaginated(ctx context.Context, channelID string, limit, offset int) ([]Conversation, int, error) {
	// Build filter — empty channelID means all conversations.
	// Non-empty channelID is matched as a prefix (e.g. "telegram:" matches all
	// telegram conversations whose id starts with that string).
	filterSQL := `(? = '' OR channel_id LIKE ?) AND deleted_at IS NULL`
	likeArg := channelID + "%"

	// Count total matching rows.
	var total int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations WHERE `+filterSQL,
		channelID, likeArg,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting conversations: %w", err)
	}

	// Fetch the requested page.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, channel_id, messages, metadata, created_at, updated_at
		 FROM conversations
		 WHERE `+filterSQL+`
		 ORDER BY updated_at DESC
		 LIMIT ? OFFSET ?`,
		channelID, likeArg, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("listing conversations paginated: %w", err)
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
			return nil, 0, fmt.Errorf("scanning conversation row: %w", err)
		}

		if err := json.Unmarshal([]byte(messagesJSON), &conv.Messages); err != nil {
			return nil, 0, fmt.Errorf("unmarshalling messages: %w", err)
		}
		if metadataJSON != "" && metadataJSON != "null" {
			if err := json.Unmarshal([]byte(metadataJSON), &conv.Metadata); err != nil {
				return nil, 0, fmt.Errorf("unmarshalling metadata: %w", err)
			}
		}

		convs = append(convs, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating conversation rows: %w", err)
	}

	if convs == nil {
		convs = []Conversation{}
	}
	return convs, total, nil
}

// CountConversations returns the total number of conversations, optionally
// filtered by channelID prefix (pass "" for all).
func (s *SQLiteStore) CountConversations(ctx context.Context, channelID string) (int, error) {
	likeArg := channelID + "%"
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM conversations WHERE (? = '' OR channel_id LIKE ?)`,
		channelID, likeArg,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting conversations: %w", err)
	}
	return count, nil
}

// DeleteConversation performs a soft delete by setting deleted_at = now().
// Returns ErrNotFound (wrapped) if no conversation with that ID exists. When
// the conv is already soft-deleted, the call is a no-op and returns nil —
// the earliest delete timestamp wins (idempotent).
func (s *SQLiteStore) DeleteConversation(ctx context.Context, scopeID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC(), scopeID,
	)
	if err != nil {
		return fmt.Errorf("deleting conversation %s: %w", scopeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for conversation %s: %w", scopeID, err)
	}
	if n == 0 {
		// Either the row is missing OR already soft-deleted. Distinguish.
		var count int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM conversations WHERE id = ?`, scopeID,
		).Scan(&count); err != nil {
			return fmt.Errorf("verifying existence of conversation %s: %w", scopeID, err)
		}
		if count == 0 {
			return fmt.Errorf("deleting conversation %s: %w", scopeID, ErrNotFound)
		}
		// Already soft-deleted — idempotent no-op.
	}
	return nil
}

// RestoreConversation clears deleted_at on a soft-deleted conv. Returns
// ErrNotFound if the conv does not exist OR is already live (the call site
// cannot restore a live conv — nothing to undo).
func (s *SQLiteStore) RestoreConversation(ctx context.Context, scopeID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE conversations SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`,
		scopeID,
	)
	if err != nil {
		return fmt.Errorf("restoring conversation %s: %w", scopeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for conversation %s: %w", scopeID, err)
	}
	if n == 0 {
		return fmt.Errorf("restoring conversation %s: %w", scopeID, ErrNotFound)
	}
	return nil
}

// DeleteConversationsOlderThan physically removes conversations whose
// deleted_at is before cutoff. Returns the count of rows deleted.
// Intended for use by the ConversationPruner on a ticker.
func (s *SQLiteStore) DeleteConversationsOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM conversations WHERE deleted_at IS NOT NULL AND deleted_at < ?`,
		cutoff.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("pruning old conversations: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetConversationMessages returns a window of messages from a single conv
// without materializing the whole row's JSON blob through the Conversation
// struct's media-touch side effects. For typical convs (<1000 msgs) this is
// load-and-slice in Go; pathological very-large convs are a documented
// follow-up that would move to json_extract('$[N]') or a messages table.
func (s *SQLiteStore) GetConversationMessages(
	ctx context.Context, id string, beforeIndex, limit int,
) ([]provider.ChatMessage, bool, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var messagesJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT messages FROM conversations WHERE id = ? AND deleted_at IS NULL`, id,
	).Scan(&messagesJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, 0, fmt.Errorf("loading conversation %s: %w", id, ErrNotFound)
		}
		return nil, false, 0, fmt.Errorf("loading conversation %s: %w", id, err)
	}

	var msgs []provider.ChatMessage
	if err := json.Unmarshal([]byte(messagesJSON), &msgs); err != nil {
		return nil, false, 0, fmt.Errorf("unmarshalling messages for conversation %s: %w", id, err)
	}

	total := len(msgs)
	if total == 0 {
		return []provider.ChatMessage{}, false, 0, nil
	}

	endExclusive := total
	if beforeIndex >= 0 && beforeIndex < total {
		endExclusive = beforeIndex
	}
	if endExclusive <= 0 {
		return []provider.ChatMessage{}, false, 0, nil
	}

	start := endExclusive - limit
	if start < 0 {
		start = 0
	}

	window := msgs[start:endExclusive]
	out := make([]provider.ChatMessage, len(window))
	copy(out, window)

	return out, start > 0, start, nil
}

// UpdateConversationTitle sets metadata.title on a conversation via JSON1's
// json_set. Trust-but-verify policy: the web-layer validator is authoritative
// for the 1..100 rune bound and newline stripping; this method only guards
// the invariants it can see locally (empty after trim → ErrInvalidTitle).
func (s *SQLiteStore) UpdateConversationTitle(ctx context.Context, id string, title string) error {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return fmt.Errorf("updating conversation title for %s: %w", id, ErrInvalidTitle)
	}
	// json_set on NULL metadata creates a new JSON object. We use
	// COALESCE(metadata, '{}') as the baseline.
	// Metadata column can be NULL, the JSON string "null" (from json.Marshal
	// of a nil map), or a real object. Normalize all three to '{}' before
	// json_set so we don't silently end up with `null` (which json_set
	// treats as a valid non-object and may return unchanged).
	res, err := s.db.ExecContext(ctx,
		`UPDATE conversations
		   SET metadata = json_set(
		        CASE
		            WHEN metadata IS NULL OR metadata = '' OR metadata = 'null' THEN '{}'
		            ELSE metadata
		        END,
		        '$.title', ?)
		 WHERE id = ? AND deleted_at IS NULL`,
		trimmed, id,
	)
	if err != nil {
		return fmt.Errorf("updating conversation title %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected updating title %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("updating conversation title %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteMemory removes a single memory entry by its rowid within scopeID.
// The memory_ad trigger fires automatically, removing the FTS5 entry.
// Returns ErrNotFound (wrapped) if no matching entry exists.
func (s *SQLiteStore) DeleteMemory(ctx context.Context, scopeID string, entryID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM memory WHERE rowid = ? AND scope_id = ?`, entryID, scopeID,
	)
	if err != nil {
		return fmt.Errorf("deleting memory entry %d: %w", entryID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for memory entry %d: %w", entryID, err)
	}
	if n == 0 {
		return fmt.Errorf("deleting memory entry %d: %w", entryID, ErrNotFound)
	}
	return nil
}

// AppendMemory adds a new memory entry under the given scopeID. The FTS5 trigger fires automatically.
func (s *SQLiteStore) AppendMemory(ctx context.Context, scopeID string, entry MemoryEntry) error {
	tagsJSON, err := json.Marshal(entry.Tags)
	if err != nil {
		return fmt.Errorf("marshalling tags: %w", err)
	}

	importance := entry.Importance
	if importance == 0 {
		importance = 5
	}
	cluster := entry.Cluster
	if cluster == "" {
		cluster = "general"
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO memory (id, scope_id, topic, type, title, content, tags, source, created_at, importance, cluster)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, scopeID, entry.Topic, entry.Type, entry.Title,
		entry.Content, string(tagsJSON), entry.Source, entry.CreatedAt, importance, cluster,
	)
	if err != nil {
		return fmt.Errorf("appending memory entry %s: %w", entry.ID, err)
	}
	return nil
}

// scanMemoryRows reads all rows from a memory query result into a slice of MemoryEntry.
// It closes rows before returning. Returns a non-nil empty slice when there are no rows.
// Scans access_count, last_accessed_at, and archived_at introduced in schema v2,
// and the embedding BLOB introduced in schema v3.
func scanMemoryRows(rows *sql.Rows) ([]MemoryEntry, error) {
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var entry MemoryEntry
		var tagsJSON string

		if err := rows.Scan(
			&entry.ID, &entry.ScopeID, &entry.Topic, &entry.Type, &entry.Title,
			&entry.Content, &tagsJSON, &entry.Source, &entry.CreatedAt,
			&entry.AccessCount, &entry.LastAccessedAt, &entry.ArchivedAt,
			&entry.Embedding, &entry.Importance, &entry.Cluster,
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

// SearchMemory searches memory entries in scopeID matching query.
//
// Search strategy:
//  1. Extract meaningful keywords from query, stripping stop words.
//  2. If keywords found: run FTS5 MATCH with BM25 ranking blended with a
//     recency penalty (0.1 * days_old), so newer entries are preferred when
//     relevance is similar.
//  3. If FTS5 returns no results OR no keywords were found: fall back to a
//     LIKE-based substring search ordered by created_at DESC.
//  4. Empty query: return all entries for scope ordered by created_at DESC.
//
// All paths exclude archived entries (archived_at IS NOT NULL). After returning
// results, access_count and last_accessed_at are updated best-effort (failures
// are logged but not propagated).
//
// limit <= 0 means no limit.
//
// Scope semantics: an empty scopeID means "all scopes" (used by the dashboard
// /api/memory endpoint to surface every memory regardless of channel). All
// non-empty scopeIDs filter to that exact scope. The agent loop and tools
// always pass a non-empty scope, so this only relaxes the contract for
// administrative read paths.
func (s *SQLiteStore) SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error) {
	var rows *sql.Rows
	var err error

	// memColumns is the SELECT list used across all query paths.
	// Includes v2 columns (access_count, last_accessed_at, archived_at) and
	// the v3 embedding BLOB column.
	const memCols = `id, scope_id, topic, type, title, content, tags, source, created_at,
	                 access_count, last_accessed_at, archived_at, embedding, importance, cluster`
	const memColsM = `m.id, m.scope_id, m.topic, m.type, m.title, m.content, m.tags, m.source, m.created_at,
	                  m.access_count, m.last_accessed_at, m.archived_at, m.embedding, m.importance, m.cluster`

	// ftsLimit is how many candidates to fetch for potential embedding reranking.
	// We fetch more than the user-requested limit to have a meaningful shortlist.
	ftsLimit := 50
	if limit > ftsLimit {
		ftsLimit = limit
	}

	// scopeFilter / scopeArgs build a conditional `scope_id = ?` clause that
	// vanishes when scopeID is empty (administrative all-scopes read).
	scopeFilter := ""
	var scopeArgs []any
	if scopeID != "" {
		scopeFilter = " AND scope_id = ?"
		scopeArgs = []any{scopeID}
	}
	scopeFilterM := strings.ReplaceAll(scopeFilter, "scope_id", "m.scope_id")

	if query == "" {
		// Empty query: return all non-archived entries (optionally scope-filtered) ordered by created_at DESC.
		q := `SELECT ` + memCols + `
		      FROM memory WHERE archived_at IS NULL` + scopeFilter + ` ORDER BY created_at DESC`
		args := append([]any{}, scopeArgs...)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	} else {
		ftsQuery := BuildFTSQuery(query)

		if ftsQuery != "" {
			// Primary path: FTS5 with BM25 + recency weighting.
			// bm25() returns negative values; more negative = worse match.
			// We subtract a small recency penalty so older entries rank worse
			// when relevance is comparable. ASC order gives best (least negative) first.
			//
			// Note: the Go driver stores time.Time as "YYYY-MM-DD HH:MM:SS +0000 UTC"
			// which SQLite's julianday() cannot parse. substr(created_at,1,19) trims to
			// "YYYY-MM-DD HH:MM:SS" which julianday() handles correctly.
			//
			// Ranking formula: bm25(memory_fts) + 0.001 * days_old
			// FTS5 bm25() returns negative values where more-negative = better relevance.
			// Adding a positive recency penalty (0.001 * days_old) pushes older entries
			// toward zero (worse), so newer entries with equal relevance rank higher.
			// MAX(0, ...) prevents future-dated entries from receiving a bonus.
			// ORDER BY ASC: smallest (most negative) value = best match comes first.
			//
			// We always fetch up to ftsLimit (50) candidates so the embedding reranker
			// has a meaningful shortlist, even when the caller only wants a few results.
			q := `SELECT ` + memColsM + `
			      FROM memory m
			      JOIN memory_fts ON memory_fts.rowid = m.rowid
			      WHERE memory_fts MATCH ? AND m.archived_at IS NULL` + scopeFilterM + `
			      ORDER BY (bm25(memory_fts) + 0.001 * MAX(0, julianday('now') - julianday(substr(m.created_at,1,19)))) ASC
			      LIMIT ?`
			args := append([]any{ftsQuery}, scopeArgs...)
			args = append(args, ftsLimit)
			rows, err = s.db.QueryContext(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("searching memory in scope %s: %w", scopeID, err)
			}

			entries, scanErr := scanMemoryRows(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			// If FTS5 found results, attempt embedding rerank then trim to limit.
			if len(entries) > 0 {
				entries = s.maybeRerank(ctx, query, entries)
				if limit > 0 && len(entries) > limit {
					entries = entries[:limit]
				}
				s.updateAccessCounts(ctx, entries)
				return entries, nil
			}
			// Fall through to LIKE fallback when FTS5 returned nothing.
		}

		// Fallback: LIKE-based substring search ordered by recency.
		// Covers the case where all query tokens were stop words (ftsQuery == "")
		// or FTS5 returned no rows. Archived entries are excluded.
		likePattern := "%" + strings.ToLower(query) + "%"
		q := `SELECT ` + memCols + `
		      FROM memory
		      WHERE archived_at IS NULL` + scopeFilter + `
		        AND (lower(content) LIKE ? OR lower(tags) LIKE ?)
		      ORDER BY created_at DESC`
		args := append([]any{}, scopeArgs...)
		args = append(args, likePattern, likePattern)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("searching memory in scope %s: %w", scopeID, err)
	}
	entries, err := scanMemoryRows(rows)
	if err != nil {
		return nil, err
	}
	s.updateAccessCounts(ctx, entries)
	return entries, nil
}

// maybeRerank applies cosine similarity reranking when conditions are met:
//  1. embedQueryFunc must be set (embedding enabled for this store).
//  2. At least 2 candidates must have non-nil embeddings.
//
// When reranking is applied, candidates are sorted by cosine similarity to the
// query embedding, descending. When conditions are not met, candidates are
// returned in their original FTS5 order.
func (s *SQLiteStore) maybeRerank(ctx context.Context, query string, candidates []MemoryEntry) []MemoryEntry {
	if s.embedQueryFunc == nil {
		return candidates
	}

	// Count candidates with embeddings.
	withEmbeddings := 0
	for _, c := range candidates {
		if len(c.Embedding) > 0 {
			withEmbeddings++
		}
	}
	if withEmbeddings < 2 {
		// Insufficient embeddings for meaningful reranking — preserve FTS5 order.
		return candidates
	}

	// Generate query embedding.
	queryVec, err := s.embedQueryFunc(ctx, query)
	if err != nil {
		// Log at debug — rerank is best-effort, fall back to FTS5 order.
		_ = fmt.Errorf("embed query for rerank (best-effort): %w", err)
		return candidates
	}
	queryNorm := normalizeEmbeddingBlob(queryVec)

	// Compute cosine similarity for each candidate that has an embedding.
	results := make([]scoredEntry, len(candidates))
	for i, c := range candidates {
		if len(c.Embedding) == 0 {
			results[i] = scoredEntry{entry: c, similarity: -2.0} // below any valid cosine
			continue
		}
		candidateVec := deserializeEmbeddingBlob(c.Embedding)
		sim := cosineSimilarity(queryNorm, candidateVec)
		results[i] = scoredEntry{entry: c, similarity: sim}
	}

	// Sort by similarity descending (stable to preserve FTS order for ties).
	sortScoredEntries(results)

	out := make([]MemoryEntry, len(results))
	for i, r := range results {
		out[i] = r.entry
	}
	return out
}

// updateAccessCounts increments access_count and sets last_accessed_at for all
// returned entries. This is best-effort: failures are logged at WARN level and
// are never propagated to the caller.
func (s *SQLiteStore) updateAccessCounts(ctx context.Context, entries []MemoryEntry) {
	if len(entries) == 0 {
		return
	}
	ids := make([]any, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1] // remove trailing comma

	query := `UPDATE memory
	          SET access_count = access_count + 1,
	              last_accessed_at = datetime('now')
	          WHERE id IN (` + placeholders + `)`

	if _, err := s.db.ExecContext(ctx, query, ids...); err != nil {
		// Best-effort: log but do not propagate.
		// Using fmt.Sprintf to avoid importing slog here; callers can wire logging.
		_ = fmt.Errorf("updating access counts (best-effort): %w", err)
	}
}

// UpdateMemory updates topic, type, title, tags, content, importance, and
// archived_at of an existing memory entry identified by entry.ID within
// scopeID. The FTS5 memory_au trigger automatically re-indexes the FTS table
// after the UPDATE.
// Returns nil if no row matched (the caller cannot distinguish "not found"
// from "no-op update" — this is by design to keep the interface simple).
func (s *SQLiteStore) UpdateMemory(ctx context.Context, scopeID string, entry MemoryEntry) error {
	tagsJSON, err := json.Marshal(entry.Tags)
	if err != nil {
		return fmt.Errorf("marshalling tags: %w", err)
	}
	cluster := entry.Cluster
	if cluster == "" {
		cluster = "general"
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE memory SET topic = ?, type = ?, title = ?, tags = ?, content = ?, importance = ?, cluster = ?, archived_at = ?
		 WHERE id = ? AND scope_id = ?`,
		entry.Topic, entry.Type, entry.Title, string(tagsJSON), entry.Content, entry.Importance, cluster, entry.ArchivedAt,
		entry.ID, scopeID,
	)
	if err != nil {
		return fmt.Errorf("updating memory entry %s: %w", entry.ID, err)
	}
	return nil
}

// ListMemoryScopes returns all distinct scope_ids that have at least one
// non-archived memory entry. Used by the Consolidator to enumerate scopes.
func (s *SQLiteStore) ListMemoryScopes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT scope_id FROM memory WHERE archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("listing memory scopes: %w", err)
	}
	defer rows.Close()

	var scopes []string
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return nil, fmt.Errorf("scanning scope row: %w", err)
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating scope rows: %w", err)
	}
	if scopes == nil {
		scopes = []string{}
	}
	return scopes, nil
}

// HasEmbedQueryFunc reports whether an embedding function has been registered.
// Used by the Curator to determine whether cosine similarity deduplication is available.
func (s *SQLiteStore) HasEmbedQueryFunc() bool {
	return s.embedQueryFunc != nil
}

// EmbedQuery generates an embedding for the given text using the registered
// embedding function. Returns an error if no embedding function is registered.
func (s *SQLiteStore) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if s.embedQueryFunc == nil {
		return nil, fmt.Errorf("embedding not configured")
	}
	return s.embedQueryFunc(ctx, text)
}

// ─── OutputStore implementation ───────────────────────────────────────────────

// escapeLike escapes backslash, percent, and underscore in s so the result
// can be used as a literal pattern in a SQL LIKE … ESCAPE '\' clause.
func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '%' || c == '_' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// IndexOutput stores a tool output in the FTS5 table for later search.
func (s *SQLiteStore) IndexOutput(ctx context.Context, output ToolOutput) error {
	if output.ID == "" {
		return ErrOutputMissingID
	}
	if output.ToolName == "" {
		return ErrOutputMissingToolName
	}
	if output.Content == "" {
		return ErrOutputEmptyContent
	}
	// Store timestamp as Unix epoch for reliable storage/retrieval
	timestampUnix := output.Timestamp.Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tool_outputs (id, tool_name, command, content, truncated, exit_code, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		output.ID,
		output.ToolName,
		output.Command,
		output.Content,
		output.Truncated,
		output.ExitCode,
		timestampUnix,
	)
	if err != nil {
		return fmt.Errorf("indexing output %s: %w", output.ID, err)
	}
	return nil
}

// SearchOutputs searches indexed tool outputs using FTS5.
// Returns matching outputs sorted by relevance (BM25), limited to limit results.
func (s *SQLiteStore) SearchOutputs(ctx context.Context, query string, limit int) ([]ToolOutput, error) {
	var rows *sql.Rows
	var err error

	const cols = `id, tool_name, command, content, truncated, exit_code, timestamp`

	if query == "" || query == "*" {
		// Return all outputs ordered by timestamp descending
		q := `SELECT ` + cols + ` FROM tool_outputs ORDER BY timestamp DESC`
		var args []any
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	} else {
		// Build FTS5 query and search
		ftsQuery := BuildFTSQuery(query)
		if ftsQuery == "" {
			// Fallback to LIKE search if no keywords.
			// escapeLike prevents user-supplied % and _ from acting as wildcards.
			likePattern := "%" + escapeLike(strings.ToLower(query)) + "%"
			q := `SELECT ` + cols + `
			      FROM tool_outputs
			      WHERE lower(content) LIKE ? ESCAPE '\' OR lower(tool_name) LIKE ? ESCAPE '\' OR lower(command) LIKE ? ESCAPE '\'
			      ORDER BY timestamp DESC`
			args := []any{likePattern, likePattern, likePattern}
			if limit > 0 {
				q += ` LIMIT ?`
				args = append(args, limit)
			}
			rows, err = s.db.QueryContext(ctx, q, args...)
		} else {
			q := `SELECT ` + cols + `
			      FROM tool_outputs
			      WHERE tool_outputs MATCH ?
			      ORDER BY bm25(tool_outputs) ASC`
			args := []any{ftsQuery}
			if limit > 0 {
				q += ` LIMIT ?`
				args = append(args, limit)
			}
			rows, err = s.db.QueryContext(ctx, q, args...)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("searching outputs: %w", err)
	}
	defer rows.Close()

	var results []ToolOutput
	for rows.Next() {
		var output ToolOutput
		var timestampUnix int64
		if err := rows.Scan(
			&output.ID,
			&output.ToolName,
			&output.Command,
			&output.Content,
			&output.Truncated,
			&output.ExitCode,
			&timestampUnix,
		); err != nil {
			return nil, fmt.Errorf("scanning output row: %w", err)
		}
		// Convert Unix epoch to time.Time
		output.Timestamp = time.Unix(timestampUnix, 0).UTC()
		results = append(results, output)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating output rows: %w", err)
	}

	if results == nil {
		results = []ToolOutput{}
	}
	return results, nil
}

// Compile-time assertions.
var (
	_ SecretsStore = (*SQLiteStore)(nil)
	_ CronStore    = (*SQLiteStore)(nil)
	_ OutputStore  = (*SQLiteStore)(nil)
)

// ─── CronStore implementation ─────────────────────────────────────────────────

// CreateJob inserts a new CronJob. The job is returned as-is (ID must be set by caller).
func (s *SQLiteStore) CreateJob(ctx context.Context, job CronJob) (CronJob, error) {
	var lastRunAt, nextRunAt *int64
	if job.LastRunAt != nil {
		v := job.LastRunAt.Unix()
		lastRunAt = &v
	}
	if job.NextRunAt != nil {
		v := job.NextRunAt.Unix()
		nextRunAt = &v
	}
	enabledInt := 0
	if job.Enabled {
		enabledInt = 1
	}
	notifyOnCompletionInt := 0
	if job.NotifyOnCompletion {
		notifyOnCompletionInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_jobs (id, schedule, schedule_human, prompt, channel_id, description, enabled, created_at, last_run_at, next_run_at, notify_channel, notify_on_completion)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Schedule, job.ScheduleHuman, job.Prompt, job.ChannelID,
		job.Description, enabledInt, job.CreatedAt.Unix(), lastRunAt, nextRunAt,
		job.NotifyChannel, notifyOnCompletionInt,
	)
	if err != nil {
		return CronJob{}, fmt.Errorf("creating cron job %s: %w", job.ID, err)
	}
	return job, nil
}

// ListJobs returns all enabled cron jobs ordered by created_at ascending.
func (s *SQLiteStore) ListJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, schedule, schedule_human, prompt, channel_id, description, enabled, created_at, last_run_at, next_run_at, notify_channel, notify_on_completion
		 FROM cron_jobs WHERE enabled = 1 ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing cron jobs: %w", err)
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		job, err := scanCronJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning cron job row: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating cron job rows: %w", err)
	}
	if jobs == nil {
		jobs = []CronJob{}
	}
	return jobs, nil
}

// GetJob retrieves a single CronJob by ID. Returns ErrNotFound (wrapped) if not present.
func (s *SQLiteStore) GetJob(ctx context.Context, id string) (CronJob, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, schedule, schedule_human, prompt, channel_id, description, enabled, created_at, last_run_at, next_run_at, notify_channel, notify_on_completion
		 FROM cron_jobs WHERE id = ?`, id,
	)
	job, err := scanCronJobRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return CronJob{}, fmt.Errorf("getting cron job %s: %w", id, ErrNotFound)
		}
		return CronJob{}, fmt.Errorf("getting cron job %s: %w", id, err)
	}
	return job, nil
}

// DeleteJob removes a CronJob by ID. Returns ErrNotFound (wrapped) if not present.
func (s *SQLiteStore) DeleteJob(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting cron job %s: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for cron job %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("deleting cron job %s: %w", id, ErrNotFound)
	}
	return nil
}

// SaveResult persists a CronResult.
func (s *SQLiteStore) SaveResult(ctx context.Context, result CronResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_results (id, job_id, ran_at, output, error_msg) VALUES (?, ?, ?, ?, ?)`,
		result.ID, result.JobID, result.RanAt.Unix(), result.Output, result.ErrorMsg,
	)
	if err != nil {
		return fmt.Errorf("saving cron result %s: %w", result.ID, err)
	}
	return nil
}

// ListResults returns the most recent results for a given jobID, newest first, limited to limit.
func (s *SQLiteStore) ListResults(ctx context.Context, jobID string, limit int) ([]CronResult, error) {
	q := `SELECT id, job_id, ran_at, output, error_msg FROM cron_results WHERE job_id = ? ORDER BY ran_at DESC`
	args := []any{jobID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing cron results for job %s: %w", jobID, err)
	}
	defer rows.Close()

	var results []CronResult
	for rows.Next() {
		var r CronResult
		var ranAtUnix int64
		if err := rows.Scan(&r.ID, &r.JobID, &ranAtUnix, &r.Output, &r.ErrorMsg); err != nil {
			return nil, fmt.Errorf("scanning cron result row: %w", err)
		}
		r.RanAt = time.Unix(ranAtUnix, 0).UTC()
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating cron result rows: %w", err)
	}
	if results == nil {
		results = []CronResult{}
	}
	return results, nil
}

// PruneResults removes cron results older than retentionDays and keeps only the
// newest maxPerJob results per job. Both limits are applied independently.
func (s *SQLiteStore) PruneResults(ctx context.Context, retentionDays, maxPerJob int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning prune transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Prune by retention age.
	if retentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM cron_results WHERE ran_at < ?`, cutoff,
		); err != nil {
			return fmt.Errorf("pruning cron results by retention: %w", err)
		}
	}

	// Prune by maxPerJob: for each job, delete results beyond the newest maxPerJob.
	if maxPerJob > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM cron_results
			 WHERE id NOT IN (
			     SELECT id FROM cron_results r2
			     WHERE r2.job_id = cron_results.job_id
			     ORDER BY ran_at DESC
			     LIMIT ?
			 )`, maxPerJob,
		); err != nil {
			return fmt.Errorf("pruning cron results by max per job: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing prune transaction: %w", err)
	}
	return nil
}

// UpdateJobRunTimes updates last_run_at and next_run_at for a cron job.
// Best-effort: no error if the job is absent.
func (s *SQLiteStore) UpdateJobRunTimes(ctx context.Context, id string, lastRunAt, nextRunAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cron_jobs SET last_run_at = ?, next_run_at = ? WHERE id = ?`,
		lastRunAt.Unix(), nextRunAt.Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("updating run times for cron job %s: %w", id, err)
	}
	return nil
}

// CountResults returns the total number of results stored for a given jobID.
func (s *SQLiteStore) CountResults(ctx context.Context, jobID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cron_results WHERE job_id = ?`, jobID,
	).Scan(&count)
	return count, err
}

// ─── CronJob scan helpers ─────────────────────────────────────────────────────

type cronJobScanner interface {
	Scan(dest ...any) error
}

func scanCronJob(s cronJobScanner) (CronJob, error) {
	var job CronJob
	var enabledInt, notifyOnCompletionInt int
	var createdAtUnix int64
	var lastRunAtUnix, nextRunAtUnix *int64

	if err := s.Scan(
		&job.ID, &job.Schedule, &job.ScheduleHuman, &job.Prompt, &job.ChannelID,
		&job.Description, &enabledInt, &createdAtUnix, &lastRunAtUnix, &nextRunAtUnix,
		&job.NotifyChannel, &notifyOnCompletionInt,
	); err != nil {
		return CronJob{}, err
	}
	job.Enabled = enabledInt != 0
	job.NotifyOnCompletion = notifyOnCompletionInt != 0
	job.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if lastRunAtUnix != nil {
		t := time.Unix(*lastRunAtUnix, 0).UTC()
		job.LastRunAt = &t
	}
	if nextRunAtUnix != nil {
		t := time.Unix(*nextRunAtUnix, 0).UTC()
		job.NextRunAt = &t
	}
	return job, nil
}

func scanCronJobRow(row *sql.Row) (CronJob, error) {
	var job CronJob
	var enabledInt, notifyOnCompletionInt int
	var createdAtUnix int64
	var lastRunAtUnix, nextRunAtUnix *int64

	if err := row.Scan(
		&job.ID, &job.Schedule, &job.ScheduleHuman, &job.Prompt, &job.ChannelID,
		&job.Description, &enabledInt, &createdAtUnix, &lastRunAtUnix, &nextRunAtUnix,
		&job.NotifyChannel, &notifyOnCompletionInt,
	); err != nil {
		return CronJob{}, err
	}
	job.Enabled = enabledInt != 0
	job.NotifyOnCompletion = notifyOnCompletionInt != 0
	job.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if lastRunAtUnix != nil {
		t := time.Unix(*lastRunAtUnix, 0).UTC()
		job.LastRunAt = &t
	}
	if nextRunAtUnix != nil {
		t := time.Unix(*nextRunAtUnix, 0).UTC()
		job.NextRunAt = &t
	}
	return job, nil
}

// encryptionKey resolves the AES-256 encryption key from config or environment.
// Resolution order: StoreConfig.EncryptionKey → DAIMON_SECRET_KEY env var.
// Returns ErrEncryptionKeyNotConfigured if neither is set.
func (s *SQLiteStore) encryptionKey() ([]byte, error) {
	hexKey := s.cfg.EncryptionKey
	if hexKey == "" {
		hexKey = os.Getenv("DAIMON_SECRET_KEY")
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
