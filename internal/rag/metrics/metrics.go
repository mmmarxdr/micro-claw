// Package metrics provides in-memory retrieval metrics collection for the RAG subsystem.
// Events are stored in a fixed-capacity ring buffer; the recorder is thread-safe.
//
// Usage:
//
//	rec := metrics.NewRingRecorder(200)
//	// inject into agent via agent.WithRAGMetrics(rec)
//	// expose via GET /api/metrics/rag
package metrics

import (
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

const maxQueryRunes = 80

// Event records the full instrumentation payload for a single RAG retrieval call.
// All timing fields are in milliseconds (0 when the corresponding path was not taken).
type Event struct {
	Timestamp               time.Time      `json:"timestamp"`
	Query                   string         `json:"query"` // truncated to 80 runes
	TotalDurationMs         int64          `json:"total_duration_ms"`
	BM25Hits                int            `json:"bm25_hits"`
	HydeEnabled             bool           `json:"hyde_enabled"`
	HydeDurationMs          int64          `json:"hyde_duration_ms"`
	HydeEmbedMs             int64          `json:"hyde_embed_ms"`
	CosineHits              int            `json:"cosine_hits"`
	NeighborsExpanded       int            `json:"neighbors_expanded"`
	ThresholdRejectedBM25   int            `json:"threshold_rejected_bm25"`
	ThresholdRejectedCosine int            `json:"threshold_rejected_cosine"`
	FinalChunksReturned     int            `json:"final_chunks_returned"`
	ProvenanceBreakdown     map[string]int `json:"provenance_breakdown"`
}

// Aggregate holds the avg, p50, and p95 for a single numeric metric field.
type Aggregate struct {
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// Aggregates holds per-field aggregates computed over the current snapshot.
type Aggregates struct {
	TotalDurationMs         Aggregate `json:"total_duration_ms"`
	BM25Hits                Aggregate `json:"bm25_hits"`
	HydeDurationMs          Aggregate `json:"hyde_duration_ms"`
	HydeEmbedMs             Aggregate `json:"hyde_embed_ms"`
	CosineHits              Aggregate `json:"cosine_hits"`
	NeighborsExpanded       Aggregate `json:"neighbors_expanded"`
	ThresholdRejectedBM25   Aggregate `json:"threshold_rejected_bm25"`
	ThresholdRejectedCosine Aggregate `json:"threshold_rejected_cosine"`
	FinalChunksReturned     Aggregate `json:"final_chunks_returned"`
}

// Recorder is the interface for recording retrieval metrics events.
// Implementations must be safe for concurrent use.
type Recorder interface {
	Record(event Event)
}

// RingRecorder is a fixed-capacity, thread-safe in-memory ring buffer.
// When capacity is exceeded the oldest event is overwritten.
type RingRecorder struct {
	mu       sync.Mutex
	buf      []Event
	cap      int
	write    int // next write position
	count    int // number of events recorded (saturates at cap)
}

// NewRingRecorder creates a new RingRecorder with the given capacity.
// Capacity must be > 0; a capacity ≤ 0 is treated as 1.
func NewRingRecorder(capacity int) *RingRecorder {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingRecorder{
		buf: make([]Event, capacity),
		cap: capacity,
	}
}

// Record adds an event to the ring buffer.
// The Query field is truncated to maxQueryRunes runes (multi-byte safe).
// If the buffer is full, the oldest event is overwritten.
func (r *RingRecorder) Record(event Event) {
	event.Query = truncateRunes(event.Query, maxQueryRunes)
	r.mu.Lock()
	r.buf[r.write] = event
	r.write = (r.write + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.mu.Unlock()
}

// Snapshot returns a copy of the current buffer contents ordered oldest-first,
// newest-last. The returned slice is safe to use after Snapshot returns.
func (r *RingRecorder) Snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return []Event{}
	}
	out := make([]Event, r.count)
	if r.count < r.cap {
		// Buffer not yet full: events are from 0..count-1 in insertion order.
		copy(out, r.buf[:r.count])
	} else {
		// Buffer full: oldest is at r.write, wrapping around.
		n := copy(out, r.buf[r.write:])
		copy(out[n:], r.buf[:r.write])
	}
	return out
}

// Aggregates computes avg/p50/p95 over the current snapshot.
// Returns a zero Aggregates struct when the buffer is empty.
func (r *RingRecorder) Aggregates() Aggregates {
	snap := r.Snapshot()
	if len(snap) == 0 {
		return Aggregates{}
	}
	return Aggregates{
		TotalDurationMs:         computeAggregate(extractInt64(snap, func(e Event) int64 { return e.TotalDurationMs })),
		BM25Hits:                computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.BM25Hits) })),
		HydeDurationMs:          computeAggregate(extractInt64(snap, func(e Event) int64 { return e.HydeDurationMs })),
		HydeEmbedMs:             computeAggregate(extractInt64(snap, func(e Event) int64 { return e.HydeEmbedMs })),
		CosineHits:              computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.CosineHits) })),
		NeighborsExpanded:       computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.NeighborsExpanded) })),
		ThresholdRejectedBM25:   computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.ThresholdRejectedBM25) })),
		ThresholdRejectedCosine: computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.ThresholdRejectedCosine) })),
		FinalChunksReturned:     computeAggregate(extractInt64(snap, func(e Event) int64 { return int64(e.FinalChunksReturned) })),
	}
}

// NoopRecorder is a zero-cost Recorder used when metrics are disabled or in
// tests that do not care about recorded events.
type NoopRecorder struct{}

// Record discards the event. Zero allocation.
func (NoopRecorder) Record(_ Event) {}

// --- helpers ----------------------------------------------------------------

// truncateRunes returns s truncated to at most n runes. Multi-byte safe.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for count := 0; count < n; count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func extractInt64(snap []Event, fn func(Event) int64) []float64 {
	out := make([]float64, len(snap))
	for i, e := range snap {
		out[i] = float64(fn(e))
	}
	return out
}

// computeAggregate returns Avg, P50, P95 for a slice of values.
// Uses sort.Float64s for O(n log n) sort — no external dependencies.
func computeAggregate(values []float64) Aggregate {
	if len(values) == 0 {
		return Aggregate{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	n := len(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	avg := sum / float64(n)

	p50 := percentile(sorted, 0.50)
	p95 := percentile(sorted, 0.95)

	return Aggregate{Avg: avg, P50: p50, P95: p95}
}

// percentile returns the value at the given percentile using floor interpolation.
// p must be in [0, 1]. sorted must be non-empty and sorted ascending.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 1 {
		return sorted[0]
	}
	idx := int(float64(n-1) * p)
	return sorted[idx]
}
