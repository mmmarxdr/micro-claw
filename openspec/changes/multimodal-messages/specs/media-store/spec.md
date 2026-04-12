# Media Store Specification

## Purpose

Defines the `MediaStore` interface, the SQLite CAS implementation backed by the `media_blobs` table, and the pruning/reference-tracking contract. `FileStore` is explicitly excluded from the implementation.

## Requirements

### Requirement: media_blobs Schema

The `media_blobs` table SHALL be created by migration v5 with the following schema:

```sql
CREATE TABLE media_blobs (
    sha256             TEXT PRIMARY KEY,
    mime               TEXT NOT NULL,
    size               INTEGER NOT NULL,
    data               BLOB NOT NULL,
    created_at         TEXT NOT NULL,
    last_referenced_at TEXT NOT NULL
);
CREATE INDEX idx_media_last_referenced ON media_blobs(last_referenced_at);
```

The migration MUST be additive. It MUST NOT alter any existing table.

#### Scenario: Migration v5 creates table on fresh database

- GIVEN a SQLite database at schema v4 or lower
- WHEN the migration runs
- THEN `media_blobs` exists with all six columns
- AND the `idx_media_last_referenced` index exists

#### Scenario: Migration v5 is idempotent

- GIVEN a SQLite database where `media_blobs` already exists
- WHEN the migration runs again
- THEN no error is returned
- AND the table is unchanged

---

### Requirement: MediaStore Interface

`internal/store` SHALL define a `MediaStore` interface with exactly four methods:

```
StoreMedia(ctx, data []byte, mime string) (sha256 string, err error)
GetMedia(ctx, sha256 string) (data []byte, mime string, err error)
TouchMedia(ctx, sha256 string) error
PruneUnreferencedMedia(ctx, olderThan time.Duration) (int, error)
```

`*SQLiteStore` MUST implement `MediaStore`. The interface MUST be the sole coupling point between the agent layer and the media store.

#### Scenario: Interface satisfied by SQLiteStore (compile-time)

- GIVEN `*SQLiteStore`
- WHEN assigned to a `MediaStore` variable
- THEN compilation succeeds

---

### Requirement: StoreMedia Returns SHA256 and Deduplicates

`StoreMedia` SHALL compute the SHA256 of `data`, insert a row via `INSERT OR IGNORE`, and return the hex SHA256. If an identical blob already exists (same SHA256), the call SHALL succeed and return the same SHA256 without modifying the existing row.

#### Scenario: StoreMedia returns sha256

- GIVEN valid bytes and a MIME type
- WHEN `StoreMedia` is called
- THEN a lowercase hex SHA256 string is returned, err is nil
- AND a row exists in `media_blobs` with that sha256

#### Scenario: StoreMedia deduplicates identical blobs

- GIVEN the same bytes stored twice
- WHEN `StoreMedia` is called a second time with identical bytes
- THEN the same sha256 is returned
- AND exactly one row exists in `media_blobs` for that sha256
- AND the second call returns no error

---

### Requirement: GetMedia Returns Bytes and MIME

`GetMedia` SHALL return the raw bytes and MIME type for a known sha256. For an unknown sha256 it SHALL return `store.ErrMediaNotFound`.

#### Scenario: GetMedia returns stored bytes

- GIVEN a blob previously stored via `StoreMedia`
- WHEN `GetMedia` is called with the returned sha256
- THEN the returned bytes are byte-for-byte identical to the original
- AND the returned mime matches what was stored

#### Scenario: Unknown sha256 returns ErrMediaNotFound

- GIVEN a sha256 string that was never stored
- WHEN `GetMedia` is called
- THEN the error is `store.ErrMediaNotFound`
- AND data is nil

---

### Requirement: TouchMedia Updates last_referenced_at

`TouchMedia` SHALL update `last_referenced_at` to the current UTC time for the given sha256. If the sha256 does not exist, it SHALL return `store.ErrMediaNotFound`.

#### Scenario: TouchMedia updates timestamp

- GIVEN a blob stored at time T
- WHEN `TouchMedia` is called at time T+1h
- THEN `last_referenced_at` in `media_blobs` is T+1h (within test tolerance)

---

### Requirement: PruneUnreferencedMedia Deletes Stale Blobs

`PruneUnreferencedMedia` SHALL delete all rows in `media_blobs` where `last_referenced_at < (now - olderThan)` AND the sha256 does not appear in any conversation's content blocks. It SHALL return the count of deleted rows.

The freshness check uses `last_referenced_at`, not `created_at`.

#### Scenario: Stale unreferenced blob is deleted

- GIVEN a blob with `last_referenced_at` older than the threshold
- AND the blob's sha256 does not appear in any conversation's messages
- WHEN `PruneUnreferencedMedia` is called
- THEN the row is deleted
- AND the return count is 1

#### Scenario: Recently referenced blob is NOT deleted

- GIVEN a blob with `last_referenced_at` within the threshold window
- WHEN `PruneUnreferencedMedia` is called
- THEN the row is NOT deleted

#### Scenario: Referenced blob is NOT deleted regardless of age

- GIVEN a blob with `last_referenced_at` older than the threshold
- AND the blob's sha256 appears in at least one conversation message
- WHEN `PruneUnreferencedMedia` is called
- THEN the row is NOT deleted

---

### Requirement: FileStore Does Not Implement MediaStore

`*FileStore` SHALL NOT implement `MediaStore`. If code that would call `StoreMedia` is invoked while using a `FileStore`, the agent SHALL log a startup warning and channels SHALL degrade to text-only behavior (media download is skipped; messages are processed as text-only).

#### Scenario: FileStore startup warning

- GIVEN the agent is configured with a FileStore backend
- WHEN the agent starts
- THEN a warning log is emitted indicating media storage is unavailable
- AND no `MediaStore` methods are called on the FileStore
