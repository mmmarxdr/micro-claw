package store

import (
	"context"
	"errors"
	"time"

	"daimon/internal/provider"
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

// MemoryEntry represents a single persisted memory item.
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

	// Fields added in schema v2 (Layer 1 migration).
	// Zero values are valid defaults for entries created before this migration.
	AccessCount    int        `json:"access_count,omitempty"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	ArchivedAt     *time.Time `json:"archived_at,omitempty"`

	// Importance is a 1–10 score assigned by the Curator during classification.
	// Default value is 5 (neutral). Added in schema v8.
	Importance int `json:"importance"`

	// Embedding stores a 256-dimensional float32 vector serialized as
	// little-endian binary (1,024 bytes). Added in schema v3.
	// Not serialized to JSON — internal transport only.
	Embedding []byte `json:"-"`
}

// Store is the primary persistence interface for conversations and memory.
type Store interface {
	SaveConversation(ctx context.Context, conv Conversation) error
	LoadConversation(ctx context.Context, id string) (*Conversation, error)
	ListConversations(ctx context.Context, channelID string, limit int) ([]Conversation, error)
	AppendMemory(ctx context.Context, scopeID string, entry MemoryEntry) error
	SearchMemory(ctx context.Context, scopeID string, query string, limit int) ([]MemoryEntry, error)

	// UpdateMemory updates the topic, type, title, tags, and content of an
	// existing memory entry identified by entry.ID within scopeID.
	// FileStore implements this as a no-op (returns nil).
	UpdateMemory(ctx context.Context, scopeID string, entry MemoryEntry) error

	Close() error
}

// CronJob represents a scheduled recurring task.
type CronJob struct {
	ID            string
	Schedule      string // 5-field cron expression
	ScheduleHuman string // human-readable description
	Prompt        string
	ChannelID     string
	Description   string
	Enabled       bool
	CreatedAt     time.Time
	LastRunAt     *time.Time
	NextRunAt     *time.Time
	NotifyChannel      string `json:"notify_channel"`       // per-job notification channel override; empty = use rule default
	NotifyOnCompletion bool   `json:"notify_on_completion"` // opt-in echo-back without needing a rule
}

// CronResult is the output of a single cron job execution.
type CronResult struct {
	ID       string
	JobID    string
	RanAt    time.Time
	Output   string
	ErrorMsg string
}

// CronStore is an optional extension to Store for scheduling support.
// Only SQLiteStore implements this; FileStore does not.
type CronStore interface {
	CreateJob(ctx context.Context, job CronJob) (CronJob, error)
	ListJobs(ctx context.Context) ([]CronJob, error)
	GetJob(ctx context.Context, id string) (CronJob, error)
	DeleteJob(ctx context.Context, id string) error
	SaveResult(ctx context.Context, result CronResult) error
	ListResults(ctx context.Context, jobID string, limit int) ([]CronResult, error)
	PruneResults(ctx context.Context, retentionDays, maxPerJob int) error
	CountResults(ctx context.Context, jobID string) (int, error)

	// UpdateJobRunTimes sets last_run_at and next_run_at for a cron job.
	// Best-effort: called after each job fire. No-op if job is absent.
	UpdateJobRunTimes(ctx context.Context, id string, lastRunAt, nextRunAt time.Time) error
}

// WebStore is an optional extension of Store for web dashboard operations.
// Only SQLiteStore implements this interface. Callers type-assert:
//
//	ws, ok := myStore.(store.WebStore)
type WebStore interface {
	// ListConversationsPaginated returns conversations filtered by channelID prefix
	// (or all if empty), ordered by updated_at descending, with pagination.
	// Returns the page slice, total count across all pages, and any error.
	ListConversationsPaginated(ctx context.Context, channelID string, limit, offset int) ([]Conversation, int, error)

	// CountConversations returns the total number of conversations, optionally
	// filtered by channelID prefix. Pass "" for all channels.
	CountConversations(ctx context.Context, channelID string) (int, error)

	// DeleteConversation removes a conversation by its ID (scope_id).
	// Returns ErrNotFound (wrapped) if no conversation with that ID exists.
	DeleteConversation(ctx context.Context, scopeID string) error

	// DeleteMemory removes a single memory entry by its rowid within scopeID.
	// Returns ErrNotFound (wrapped) if no matching entry exists.
	DeleteMemory(ctx context.Context, scopeID string, entryID int64) error
}

// CostRecord represents a single LLM call cost record.
type CostRecord struct {
	ID            string
	SessionID     string
	ChannelID     string
	Model         string
	InputTokens   int
	OutputTokens  int
	InputCostUSD  float64
	OutputCostUSD float64
	TotalCostUSD  float64
	Timestamp     time.Time
}

// CostFilter allows filtering cost records by dimension.
type CostFilter struct {
	SessionID string
	ChannelID string
	Model     string
	Since     time.Time
	Until     time.Time
}

// CostModelCost represents aggregated costs for a single model.
type CostModelCost struct {
	Model        string
	InputTokens  int
	OutputTokens int
	TotalCostUSD float64
	CallCount    int
}

// CostSummary represents aggregated cost data across records.
type CostSummary struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCostUSD      float64
	RecordCount       int
	ByModel           []CostModelCost
}

// CostStore is an optional extension for cost tracking.
// Only SQLiteStore implements this; callers type-assert.
type CostStore interface {
	RecordCost(ctx context.Context, record CostRecord) error
	GetCostSummary(ctx context.Context, filter CostFilter) (CostSummary, error)
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
