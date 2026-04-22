package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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

	var lastAccessed any
	if doc.LastAccessedAt != nil && !doc.LastAccessedAt.IsZero() {
		lastAccessed = doc.LastAccessedAt.UTC().Format(time.RFC3339)
	}
	var pageCount any
	if doc.PageCount != nil {
		pageCount = *doc.PageCount
	}
	var ingested any
	if doc.IngestedAt != nil && !doc.IngestedAt.IsZero() {
		ingested = doc.IngestedAt.UTC().Format(time.RFC3339)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO documents
			(id, namespace, title, source_sha256, mime, chunk_count, created_at, updated_at,
			 access_count, last_accessed_at, summary, page_count, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Namespace, doc.Title, doc.SourceSHA256, doc.MIME,
		doc.ChunkCount, doc.CreatedAt.Format(time.RFC3339), doc.UpdatedAt.Format(time.RFC3339),
		doc.AccessCount, lastAccessed, doc.Summary, pageCount, ingested,
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
// opts.Limit results ordered by relevance descending.
//
// When opts.NeighborRadius > 0, each primary hit is expanded with adjacent
// chunks (±radius within the same document). De-duplication is by chunk ID.
// Neighbors inherit the primary hit's score.
//
// opts.MaxBM25Score filters on the BM25 path (lower/more-negative = better;
// reject when bm25() > threshold). Zero = disabled.
// opts.MinCosineScore filters on the cosine path; reject when cosine < threshold.
// Zero = disabled.
func (s *SQLiteDocumentStore) SearchChunks(ctx context.Context, query string, queryVec []float32, opts SearchOptions) ([]SearchResult, error) {
	if opts.SkipFTS {
		return s.pureVectorSearch(ctx, queryVec, opts)
	}

	limit := opts.Limit
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
		        COALESCE(d.title, '') AS doc_title,
		        bm25(document_chunks_fts) AS bm25_score
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
		chunk     DocumentChunk
		docTitle  string
		bm25Score float64
	}

	var candidates []candidate
	for rows.Next() {
		var c candidate
		var embBlob []byte
		if err := rows.Scan(
			&c.chunk.ID, &c.chunk.DocID, &c.chunk.Index,
			&c.chunk.Content, &embBlob, &c.chunk.TokenCount,
			&c.docTitle, &c.bm25Score,
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

			// Apply MinCosineScore threshold (cosine path only).
			primaries := make([]SearchResult, 0, min(len(scoredList), limit))
			for _, sc := range scoredList {
				if len(primaries) >= limit {
					break
				}
				if opts.MinCosineScore > 0 && sc.score < opts.MinCosineScore {
					continue
				}
				primaries = append(primaries, SearchResult{
					Chunk:    sc.c.chunk,
					DocTitle: sc.c.docTitle,
					Score:    sc.score,
				})
			}

			results, err := s.expandNeighbors(ctx, primaries, opts.NeighborRadius)
			if err != nil {
				return nil, err
			}
			s.bumpAccessCounters(ctx, results)
			return results, nil
		}
	}

	// No cosine rerank — apply BM25 threshold and return FTS5 results trimmed to limit.
	primaries := make([]SearchResult, 0, min(len(candidates), limit))
	for _, c := range candidates {
		if len(primaries) >= limit {
			break
		}
		// Apply MaxBM25Score threshold (BM25 path only; lower/more-negative = better).
		if opts.MaxBM25Score != 0 && c.bm25Score > opts.MaxBM25Score {
			continue
		}
		primaries = append(primaries, SearchResult{
			Chunk:    c.chunk,
			DocTitle: c.docTitle,
		})
	}

	results, err := s.expandNeighbors(ctx, primaries, opts.NeighborRadius)
	if err != nil {
		return nil, err
	}
	s.bumpAccessCounters(ctx, results)
	return results, nil
}

// pureVectorSearch does cosine-only search against all chunks with embeddings.
// No FTS5 prefilter. Iterates every chunk with a non-null embedding, computes
// cosine similarity against queryVec, sorts top-Limit descending, applies
// MinCosineScore threshold, and expands NeighborRadius.
//
// Returns nil, nil when queryVec is empty — pure-vector search requires a vector.
func (s *SQLiteDocumentStore) pureVectorSearch(ctx context.Context, queryVec []float32, opts SearchOptions) ([]SearchResult, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT dc.id, dc.doc_id, dc.idx, dc.content, dc.embedding, dc.token_count,
		        COALESCE(d.title, '') AS doc_title
		 FROM document_chunks dc
		 LEFT JOIN documents d ON d.id = dc.doc_id
		 WHERE dc.embedding IS NOT NULL AND length(dc.embedding) > 0`,
	)
	if err != nil {
		return nil, fmt.Errorf("rag: pure-vector scan: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		chunk    DocumentChunk
		docTitle string
		score    float64
	}

	normQuery := NormalizeEmbedding(queryVec, 256)
	var scored []candidate

	for rows.Next() {
		var ch DocumentChunk
		var embBlob []byte
		var docTitle string
		if err := rows.Scan(
			&ch.ID, &ch.DocID, &ch.Index,
			&ch.Content, &embBlob, &ch.TokenCount,
			&docTitle,
		); err != nil {
			return nil, fmt.Errorf("rag: pure-vector scan row: %w", err)
		}
		if len(embBlob) == 0 {
			continue
		}
		ch.Embedding = DeserializeEmbedding(embBlob)
		norm := NormalizeEmbedding(ch.Embedding, 256)
		sc := CosineSimilarity(normQuery, norm)
		scored = append(scored, candidate{chunk: ch, docTitle: docTitle, score: sc})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: pure-vector scan rows: %w", err)
	}

	// Sort descending by cosine score.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Apply MinCosineScore threshold and cap at limit.
	primaries := make([]SearchResult, 0, min(len(scored), limit))
	for _, c := range scored {
		if len(primaries) >= limit {
			break
		}
		if opts.MinCosineScore > 0 && c.score < opts.MinCosineScore {
			continue
		}
		cosine := c.score
		primaries = append(primaries, SearchResult{
			Chunk:       c.chunk,
			DocTitle:    c.docTitle,
			Score:       c.score,
			CosineScore: &cosine,
		})
	}

	results, err := s.expandNeighbors(ctx, primaries, opts.NeighborRadius)
	if err != nil {
		return nil, err
	}
	s.bumpAccessCounters(ctx, results)
	return results, nil
}

// expandNeighbors expands each primary SearchResult with adjacent chunks
// (within ±radius of the same document). Neighbors inherit the primary's score.
// De-duplication is by chunk ID. When radius <= 0, primaries are returned as-is.
func (s *SQLiteDocumentStore) expandNeighbors(ctx context.Context, primaries []SearchResult, radius int) ([]SearchResult, error) {
	if radius <= 0 || len(primaries) == 0 {
		return primaries, nil
	}

	// Build a set of already-included chunk IDs and group primaries by doc.
	seen := make(map[string]struct{}, len(primaries)*3)
	// doc → list of windows to fetch
	type fetchReq struct {
		docID string
		lo    int
		hi    int
		score float64
		title string
	}
	var fetches []fetchReq

	for _, r := range primaries {
		seen[r.Chunk.ID] = struct{}{}
		fetches = append(fetches, fetchReq{
			docID: r.Chunk.DocID,
			lo:    r.Chunk.Index - radius,
			hi:    r.Chunk.Index + radius,
			score: r.Score,
			title: r.DocTitle,
		})
	}

	// Clamp lo to 0 (SQL BETWEEN handles the upper bound naturally since there
	// are no idx values beyond the actual chunks).
	type neighbor struct {
		chunk    DocumentChunk
		docTitle string
		score    float64
	}
	var neighbors []neighbor

	for _, f := range fetches {
		lo := f.lo
		if lo < 0 {
			lo = 0
		}

		rows, err := s.db.QueryContext(ctx,
			`SELECT dc.id, dc.doc_id, dc.idx, dc.content, dc.embedding, dc.token_count,
			        COALESCE(d.title, '') AS doc_title
			 FROM document_chunks dc
			 LEFT JOIN documents d ON d.id = dc.doc_id
			 WHERE dc.doc_id = ? AND dc.idx BETWEEN ? AND ?
			 ORDER BY dc.idx ASC`,
			f.docID, lo, f.hi,
		)
		if err != nil {
			return nil, fmt.Errorf("rag: neighbor fetch for doc %s: %w", f.docID, err)
		}

		for rows.Next() {
			var ch DocumentChunk
			var embBlob []byte
			var docTitle string
			if err := rows.Scan(
				&ch.ID, &ch.DocID, &ch.Index,
				&ch.Content, &embBlob, &ch.TokenCount,
				&docTitle,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("rag: scan neighbor row: %w", err)
			}
			if len(embBlob) > 0 {
				ch.Embedding = DeserializeEmbedding(embBlob)
			}
			if _, ok := seen[ch.ID]; ok {
				continue // already in primaries or a prior neighbor fetch
			}
			seen[ch.ID] = struct{}{}
			neighbors = append(neighbors, neighbor{chunk: ch, docTitle: docTitle, score: f.score})
		}
		rows.Close()
	}

	// Merge primaries and neighbors. Build a map from chunk ID to its primary
	// position so we can interleave neighbors in document order.
	//
	// Strategy: emit all results sorted by (docID, idx) within each cluster,
	// keeping primaries with their inherited-score neighbors. For simplicity we
	// collect everything and sort by (docID, idx).
	all := make([]SearchResult, 0, len(primaries)+len(neighbors))
	all = append(all, primaries...)
	for _, n := range neighbors {
		all = append(all, SearchResult{
			Chunk:    n.chunk,
			DocTitle: n.docTitle,
			Score:    n.score,
		})
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Chunk.DocID != all[j].Chunk.DocID {
			return all[i].Chunk.DocID < all[j].Chunk.DocID
		}
		return all[i].Chunk.Index < all[j].Chunk.Index
	})

	return all, nil
}

// bumpAccessCounters increments access_count and sets last_accessed_at for the
// distinct parent documents of the supplied results. Best-effort: failures are
// logged at debug level and never propagated — a logged failure must not mask
// a successful search.
func (s *SQLiteDocumentStore) bumpAccessCounters(ctx context.Context, results []SearchResult) {
	if len(results) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(results))
	ids := make([]any, 0, len(results))
	for _, r := range results {
		if r.Chunk.DocID == "" {
			continue
		}
		if _, ok := seen[r.Chunk.DocID]; ok {
			continue
		}
		seen[r.Chunk.DocID] = struct{}{}
		ids = append(ids, r.Chunk.DocID)
	}
	if len(ids) == 0 {
		return
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := append([]any{time.Now().UTC().Format(time.RFC3339)}, ids...)
	q := `UPDATE documents
	      SET access_count = access_count + 1,
	          last_accessed_at = ?
	      WHERE id IN (` + placeholders + `)`
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		slog.Debug("rag: bump access counters (best-effort)", "error", err)
	}
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

	const docCols = `id, namespace, title, COALESCE(source_sha256,''), COALESCE(mime,''),
	                 chunk_count, created_at, updated_at,
	                 access_count, last_accessed_at, COALESCE(summary,''), page_count, ingested_at`
	if namespace == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+docCols+` FROM documents ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+docCols+` FROM documents WHERE namespace = ? ORDER BY created_at DESC`,
			namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("rag: list documents: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		d, scanErr := scanDocumentRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: iterating document rows: %w", err)
	}
	return docs, nil
}

// SumTokensByDoc returns total token_count summed per document ID, for the
// supplied doc IDs. Empty or nil input returns an empty map. Docs with no
// chunks are absent from the result — callers should treat missing keys as 0.
func (s *SQLiteDocumentStore) SumTokensByDoc(ctx context.Context, docIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(docIDs))
	if len(docIDs) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(docIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		args[i] = id
	}
	q := `SELECT doc_id, COALESCE(SUM(token_count), 0)
	      FROM document_chunks
	      WHERE doc_id IN (` + placeholders + `)
	      GROUP BY doc_id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("rag: sum tokens by doc: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var total int
		if err := rows.Scan(&id, &total); err != nil {
			return nil, fmt.Errorf("rag: scan token sum row: %w", err)
		}
		out[id] = total
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: iterating token sum rows: %w", err)
	}
	return out, nil
}

// GetDocument returns a single document by ID. Returns ErrDocNotFound when no
// row matches.
func (s *SQLiteDocumentStore) GetDocument(ctx context.Context, id string) (Document, error) {
	const docCols = `id, namespace, title, COALESCE(source_sha256,''), COALESCE(mime,''),
	                 chunk_count, created_at, updated_at,
	                 access_count, last_accessed_at, COALESCE(summary,''), page_count, ingested_at`
	row := s.db.QueryRowContext(ctx,
		`SELECT `+docCols+` FROM documents WHERE id = ?`, id)
	d, err := scanDocumentRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Document{}, fmt.Errorf("%w: %s", ErrDocNotFound, id)
		}
		return Document{}, err
	}
	return d, nil
}

// scanDocumentRow reads a single document row using the canonical column order
// expected by docCols (ListDocuments, GetDocument).
func scanDocumentRow(r interface {
	Scan(dest ...any) error
}) (Document, error) {
	var d Document
	var createdStr, updatedStr string
	var lastAccessedStr, ingestedStr sql.NullString
	var pageCount sql.NullInt64
	if err := r.Scan(
		&d.ID, &d.Namespace, &d.Title, &d.SourceSHA256, &d.MIME,
		&d.ChunkCount, &createdStr, &updatedStr,
		&d.AccessCount, &lastAccessedStr, &d.Summary, &pageCount, &ingestedStr,
	); err != nil {
		return Document{}, fmt.Errorf("rag: scan document row: %w", err)
	}
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	if lastAccessedStr.Valid {
		if t, err := time.Parse(time.RFC3339, lastAccessedStr.String); err == nil {
			d.LastAccessedAt = &t
		}
	}
	if pageCount.Valid {
		pc := int(pageCount.Int64)
		d.PageCount = &pc
	}
	if ingestedStr.Valid {
		if t, err := time.Parse(time.RFC3339, ingestedStr.String); err == nil {
			d.IngestedAt = &t
		}
	}
	return d, nil
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
