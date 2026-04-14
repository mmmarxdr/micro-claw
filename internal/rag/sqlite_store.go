package rag

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SQLiteDocumentStore implements DocumentStore backed by a SQLite database.
// Create via NewSQLiteDocumentStore; the caller is responsible for running
// MigrateV9 on the database before constructing this store.
type SQLiteDocumentStore struct {
	db        *sql.DB
	maxDocs   int
	maxChunks int
}

// NewSQLiteDocumentStore constructs a store that enforces the given document
// and chunk limits.  Pass 0 for either limit to disable that guard.
func NewSQLiteDocumentStore(db *sql.DB, maxDocs, maxChunks int) *SQLiteDocumentStore {
	return &SQLiteDocumentStore{
		db:        db,
		maxDocs:   maxDocs,
		maxChunks: maxChunks,
	}
}

// AddDocument inserts or replaces a Document record.  When maxDocs > 0 it
// rejects the insert if the current document count would exceed the limit.
func (s *SQLiteDocumentStore) AddDocument(ctx context.Context, doc Document) error {
	if s.maxDocs > 0 {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`).Scan(&count); err != nil {
			return fmt.Errorf("rag: count documents: %w", err)
		}
		if count >= s.maxDocs {
			return fmt.Errorf("%w (limit %d)", ErrStorageLimitReached, s.maxDocs)
		}
	}

	now := time.Now().UTC()
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	if doc.UpdatedAt.IsZero() {
		doc.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO documents
			(id, namespace, title, source_sha256, mime, chunk_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Namespace, doc.Title, doc.SourceSHA256, doc.MIME,
		doc.ChunkCount, doc.CreatedAt.Format(time.RFC3339), doc.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("rag: insert document %s: %w", doc.ID, err)
	}
	return nil
}

// AddChunks inserts a batch of DocumentChunks for the given document and
// updates the document's chunk_count to reflect the total stored.
func (s *SQLiteDocumentStore) AddChunks(ctx context.Context, docID string, chunks []DocumentChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rag: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO document_chunks (id, doc_id, idx, content, embedding, token_count)
		 VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("rag: prepare insert chunk: %w", err)
	}
	defer stmt.Close()

	for _, c := range chunks {
		var blob []byte
		if len(c.Embedding) > 0 {
			blob = SerializeEmbedding(c.Embedding)
		}
		if _, err := stmt.ExecContext(ctx, c.ID, docID, c.Index, c.Content, blob, c.TokenCount); err != nil {
			return fmt.Errorf("rag: insert chunk %s: %w", c.ID, err)
		}
	}

	// Update chunk_count on the parent document.
	if _, err := tx.ExecContext(ctx,
		`UPDATE documents SET chunk_count = (
			SELECT COUNT(*) FROM document_chunks WHERE doc_id = ?
		), updated_at = ? WHERE id = ?`,
		docID, time.Now().UTC().Format(time.RFC3339), docID,
	); err != nil {
		return fmt.Errorf("rag: update chunk_count for %s: %w", docID, err)
	}

	return tx.Commit()
}

// SearchChunks performs FTS5 full-text search on query, then optionally
// reranks candidates by cosine similarity against queryVec.  Returns up to
// limit results ordered by relevance descending.
func (s *SQLiteDocumentStore) SearchChunks(ctx context.Context, query string, queryVec []float32, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsLimit := 50
	if limit > ftsLimit {
		ftsLimit = limit
	}

	ftsQuery := sanitizeFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT dc.id, dc.doc_id, dc.idx, dc.content, dc.embedding, dc.token_count,
		        COALESCE(d.title, '') AS doc_title
		 FROM document_chunks dc
		 JOIN document_chunks_fts fts ON fts.rowid = dc.rowid
		 LEFT JOIN documents d ON d.id = dc.doc_id
		 WHERE document_chunks_fts MATCH ?
		 ORDER BY bm25(document_chunks_fts) ASC
		 LIMIT ?`,
		ftsQuery, ftsLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("rag: FTS5 search: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		chunk    DocumentChunk
		docTitle string
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		var embBlob []byte
		if err := rows.Scan(
			&c.chunk.ID, &c.chunk.DocID, &c.chunk.Index,
			&c.chunk.Content, &embBlob, &c.chunk.TokenCount,
			&c.docTitle,
		); err != nil {
			return nil, fmt.Errorf("rag: scan chunk row: %w", err)
		}
		if len(embBlob) > 0 {
			c.chunk.Embedding = DeserializeEmbedding(embBlob)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: iterating chunk rows: %w", err)
	}

	// Cosine rerank when queryVec is provided and at least 2 candidates have embeddings.
	if len(queryVec) > 0 {
		embedded := 0
		for _, c := range candidates {
			if len(c.chunk.Embedding) > 0 {
				embedded++
			}
		}
		if embedded >= 2 {
			type scored struct {
				c     candidate
				score float64
			}
			scoredList := make([]scored, len(candidates))
			normQuery := NormalizeEmbedding(queryVec, 256)
			for i, c := range candidates {
				var sc float64
				if len(c.chunk.Embedding) > 0 {
					norm := NormalizeEmbedding(c.chunk.Embedding, 256)
					sc = CosineSimilarity(normQuery, norm)
				}
				scoredList[i] = scored{c: c, score: sc}
			}
			sort.SliceStable(scoredList, func(i, j int) bool {
				return scoredList[i].score > scoredList[j].score
			})
			results := make([]SearchResult, 0, min(len(scoredList), limit))
			for _, sc := range scoredList {
				if len(results) >= limit {
					break
				}
				results = append(results, SearchResult{
					Chunk:    sc.c.chunk,
					DocTitle: sc.c.docTitle,
					Score:    sc.score,
				})
			}
			return results, nil
		}
	}

	// No cosine rerank — return FTS5 results trimmed to limit.
	results := make([]SearchResult, 0, min(len(candidates), limit))
	for i, c := range candidates {
		if i >= limit {
			break
		}
		results = append(results, SearchResult{
			Chunk:    c.chunk,
			DocTitle: c.docTitle,
		})
	}
	return results, nil
}

// DeleteDocument removes a document (and all its chunks via CASCADE).
func (s *SQLiteDocumentStore) DeleteDocument(ctx context.Context, docID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, docID)
	if err != nil {
		return fmt.Errorf("rag: delete document %s: %w", docID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrDocNotFound, docID)
	}
	return nil
}

// ListDocuments returns all documents in namespace.  An empty namespace
// returns all documents across all namespaces.
func (s *SQLiteDocumentStore) ListDocuments(ctx context.Context, namespace string) ([]Document, error) {
	var (
		rows *sql.Rows
		err  error
	)

	if namespace == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, namespace, title, COALESCE(source_sha256,''), COALESCE(mime,''),
			        chunk_count, created_at, updated_at
			 FROM documents ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, namespace, title, COALESCE(source_sha256,''), COALESCE(mime,''),
			        chunk_count, created_at, updated_at
			 FROM documents WHERE namespace = ?
			 ORDER BY created_at DESC`,
			namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("rag: list documents: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var d Document
		var createdStr, updatedStr string
		if err := rows.Scan(&d.ID, &d.Namespace, &d.Title, &d.SourceSHA256, &d.MIME,
			&d.ChunkCount, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("rag: scan document row: %w", err)
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		d.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: iterating document rows: %w", err)
	}
	return docs, nil
}

// sanitizeFTSQuery converts a raw user query to an FTS5 MATCH expression.
// It strips FTS5 special characters and joins clean tokens with OR, so that
// any single keyword match returns results.
func sanitizeFTSQuery(query string) string {
	// Replace common FTS5 operators/special chars with spaces.
	replacer := strings.NewReplacer(
		`"`, ` `, `'`, ` `, `(`, ` `, `)`, ` `,
		`*`, ` `, `^`, ` `, `+`, ` `, `-`, ` `,
		`:`, ` `, `\`, ` `, `{`, ` `, `}`, ` `,
	)
	cleaned := replacer.Replace(query)

	tokens := strings.Fields(cleaned)
	var keywords []string
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if len(t) >= 2 {
			keywords = append(keywords, t)
		}
	}
	if len(keywords) == 0 {
		return ""
	}
	return strings.Join(keywords, " OR ")
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
