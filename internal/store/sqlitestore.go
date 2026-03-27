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

CREATE TABLE IF NOT EXISTS cron_jobs (
	id             TEXT PRIMARY KEY,
	schedule       TEXT NOT NULL,
	schedule_human TEXT NOT NULL,
	prompt         TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	enabled        INTEGER NOT NULL DEFAULT 1,
	created_at     INTEGER NOT NULL,
	last_run_at    INTEGER,
	next_run_at    INTEGER
);

CREATE TABLE IF NOT EXISTS cron_results (
	id        TEXT PRIMARY KEY,
	job_id    TEXT NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
	ran_at    INTEGER NOT NULL,
	output    TEXT,
	error_msg TEXT
);
CREATE INDEX IF NOT EXISTS idx_cron_results_job_ran ON cron_results(job_id, ran_at DESC);
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

// scanMemoryRows reads all rows from a memory query result into a slice of MemoryEntry.
// It closes rows before returning. Returns a non-nil empty slice when there are no rows.
func scanMemoryRows(rows *sql.Rows) ([]MemoryEntry, error) {
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
			q := `SELECT m.id, m.scope_id, m.topic, m.type, m.title, m.content, m.tags, m.source, m.created_at
			      FROM memory m
			      JOIN memory_fts ON memory_fts.rowid = m.rowid
			      WHERE memory_fts MATCH ? AND m.scope_id = ?
			      ORDER BY (bm25(memory_fts) + 0.001 * MAX(0, julianday('now') - julianday(substr(m.created_at,1,19)))) ASC`
			args := []any{ftsQuery, scopeID}
			if limit > 0 {
				q += ` LIMIT ?`
				args = append(args, limit)
			}
			rows, err = s.db.QueryContext(ctx, q, args...)
			if err != nil {
				return nil, fmt.Errorf("searching memory in scope %s: %w", scopeID, err)
			}

			entries, scanErr := scanMemoryRows(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			// If FTS5 found results, return them directly.
			if len(entries) > 0 {
				return entries, nil
			}
			// Fall through to LIKE fallback when FTS5 returned nothing.
		}

		// Fallback: LIKE-based substring search ordered by recency.
		// Covers the case where all query tokens were stop words (ftsQuery == "")
		// or FTS5 returned no rows.
		likePattern := "%" + strings.ToLower(query) + "%"
		q := `SELECT id, scope_id, topic, type, title, content, tags, source, created_at
		      FROM memory
		      WHERE scope_id = ? AND (lower(content) LIKE ? OR lower(tags) LIKE ?)
		      ORDER BY created_at DESC`
		args := []any{scopeID, likePattern, likePattern}
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err = s.db.QueryContext(ctx, q, args...)
	}

	if err != nil {
		return nil, fmt.Errorf("searching memory in scope %s: %w", scopeID, err)
	}
	return scanMemoryRows(rows)
}

// Compile-time assertions.
var _ SecretsStore = (*SQLiteStore)(nil)
var _ CronStore = (*SQLiteStore)(nil)

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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cron_jobs (id, schedule, schedule_human, prompt, channel_id, enabled, created_at, last_run_at, next_run_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Schedule, job.ScheduleHuman, job.Prompt, job.ChannelID,
		enabledInt, job.CreatedAt.Unix(), lastRunAt, nextRunAt,
	)
	if err != nil {
		return CronJob{}, fmt.Errorf("creating cron job %s: %w", job.ID, err)
	}
	return job, nil
}

// ListJobs returns all enabled cron jobs ordered by created_at ascending.
func (s *SQLiteStore) ListJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, schedule, schedule_human, prompt, channel_id, enabled, created_at, last_run_at, next_run_at
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
		`SELECT id, schedule, schedule_human, prompt, channel_id, enabled, created_at, last_run_at, next_run_at
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

// ─── CronJob scan helpers ─────────────────────────────────────────────────────

type cronJobScanner interface {
	Scan(dest ...any) error
}

func scanCronJob(s cronJobScanner) (CronJob, error) {
	var job CronJob
	var enabledInt int
	var createdAtUnix int64
	var lastRunAtUnix, nextRunAtUnix *int64

	if err := s.Scan(
		&job.ID, &job.Schedule, &job.ScheduleHuman, &job.Prompt, &job.ChannelID,
		&enabledInt, &createdAtUnix, &lastRunAtUnix, &nextRunAtUnix,
	); err != nil {
		return CronJob{}, err
	}
	job.Enabled = enabledInt != 0
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
	var enabledInt int
	var createdAtUnix int64
	var lastRunAtUnix, nextRunAtUnix *int64

	if err := row.Scan(
		&job.ID, &job.Schedule, &job.ScheduleHuman, &job.Prompt, &job.ChannelID,
		&enabledInt, &createdAtUnix, &lastRunAtUnix, &nextRunAtUnix,
	); err != nil {
		return CronJob{}, err
	}
	job.Enabled = enabledInt != 0
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
