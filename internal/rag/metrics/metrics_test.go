package metrics_test

import (
	"sync"
	"testing"
	"time"

	. "daimon/internal/rag/metrics"
)

// ---------------------------------------------------------------------------
// M1: NewRingRecorder with capacity returns empty Snapshot.
// ---------------------------------------------------------------------------

func TestNewRingRecorder_EmptySnapshot(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(10)
	snap := r.Snapshot()
	if len(snap) != 0 {
		t.Errorf("M1: expected empty snapshot, got %d events", len(snap))
	}
}

// ---------------------------------------------------------------------------
// M2: Record below capacity retains all events, newest-last.
// ---------------------------------------------------------------------------

func TestRingRecorder_BelowCapacity(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(10)
	for i := 0; i < 5; i++ {
		r.Record(Event{
			Timestamp: time.Now(),
			Query:     string(rune('a' + i)),
		})
	}
	snap := r.Snapshot()
	if len(snap) != 5 {
		t.Fatalf("M2: expected 5 events, got %d", len(snap))
	}
	// Newest-last: first recorded is at index 0 in the snapshot ordering
	// (oldest first, newest last — Snapshot returns insertion order within buffer).
	if snap[4].Query != "e" {
		t.Errorf("M2: last event should be newest, got Query=%q", snap[4].Query)
	}
}

// ---------------------------------------------------------------------------
// M3: Record beyond capacity evicts oldest (circular behavior).
// ---------------------------------------------------------------------------

func TestRingRecorder_ExceedsCapacity(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(3)
	for i := 0; i < 5; i++ {
		r.Record(Event{
			Timestamp: time.Now(),
			Query:     string(rune('a' + i)),
		})
	}
	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("M3: expected 3 events (capacity), got %d", len(snap))
	}
	// Oldest (a, b) should be evicted; remaining are c, d, e
	queries := make([]string, len(snap))
	for i, e := range snap {
		queries[i] = e.Query
	}
	// The ring should contain the last 3 recorded events
	expectedLast := []string{"c", "d", "e"}
	for i, q := range queries {
		if q != expectedLast[i] {
			t.Errorf("M3: snap[%d].Query = %q, want %q", i, q, expectedLast[i])
		}
	}
}

// ---------------------------------------------------------------------------
// M4: Query field truncated to 80 runes at Record time, original untouched.
// ---------------------------------------------------------------------------

func TestRingRecorder_QueryTruncatedTo80Runes(t *testing.T) {
	t.Parallel()
	// Build a 200-rune multi-byte string (Japanese characters are 3 bytes each).
	runes := make([]rune, 200)
	for i := range runes {
		runes[i] = '本' // 3 bytes in UTF-8
	}
	original := string(runes)

	r := NewRingRecorder(10)
	r.Record(Event{Query: original})

	snap := r.Snapshot()
	if len(snap) == 0 {
		t.Fatal("M4: snapshot is empty")
	}

	truncated := snap[0].Query
	runeCount := len([]rune(truncated))
	if runeCount > 80 {
		t.Errorf("M4: truncated query has %d runes, want ≤80", runeCount)
	}
	// The original variable is unchanged (caller owns it).
	if len([]rune(original)) != 200 {
		t.Errorf("M4: original modified — should not happen")
	}
	// No mojibake: last rune must be valid.
	for _, r := range truncated {
		if r == '�' {
			t.Error("M4: truncated query contains replacement character (broken multi-byte)")
		}
	}
}

// ---------------------------------------------------------------------------
// M5: Aggregates over empty buffer returns zero struct (no panic).
// ---------------------------------------------------------------------------

func TestRingRecorder_AggregatesEmpty(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(10)
	agg := r.Aggregates()
	if agg.TotalDurationMs.Avg != 0 || agg.TotalDurationMs.P50 != 0 || agg.TotalDurationMs.P95 != 0 {
		t.Errorf("M5: expected zero aggregates, got %+v", agg.TotalDurationMs)
	}
	if agg.FinalChunksReturned.P95 != 0 {
		t.Errorf("M5: expected zero FinalChunksReturned, got %+v", agg.FinalChunksReturned)
	}
}

// ---------------------------------------------------------------------------
// M6: Aggregates over single event: avg=p50=p95=that event's values.
// ---------------------------------------------------------------------------

func TestRingRecorder_AggregatesSingleEvent(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(10)
	r.Record(Event{
		TotalDurationMs:   100,
		FinalChunksReturned: 5,
	})
	agg := r.Aggregates()
	if agg.TotalDurationMs.Avg != 100 {
		t.Errorf("M6: TotalDurationMs.Avg: want 100, got %v", agg.TotalDurationMs.Avg)
	}
	if agg.TotalDurationMs.P50 != 100 {
		t.Errorf("M6: TotalDurationMs.P50: want 100, got %v", agg.TotalDurationMs.P50)
	}
	if agg.TotalDurationMs.P95 != 100 {
		t.Errorf("M6: TotalDurationMs.P95: want 100, got %v", agg.TotalDurationMs.P95)
	}
	if agg.FinalChunksReturned.P50 != 5 {
		t.Errorf("M6: FinalChunksReturned.P50: want 5, got %v", agg.FinalChunksReturned.P50)
	}
}

// ---------------------------------------------------------------------------
// M7: Aggregates over 10 events: p50 and p95 match hand-computed values.
//
// Values: 1..10
// avg = 5.5
// p50 = index floor((10-1)*0.50) = 4 → value 5 (0-indexed after sort)
// p95 = index floor((10-1)*0.95) = 8 → value 9
// ---------------------------------------------------------------------------

func TestRingRecorder_AggregatesTenEvents(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(20)
	// Record in non-sorted order to ensure sort is applied.
	values := []int64{5, 3, 1, 10, 7, 2, 8, 4, 9, 6}
	for _, v := range values {
		r.Record(Event{TotalDurationMs: v})
	}
	agg := r.Aggregates()

	// avg = (1+2+…+10)/10 = 55/10 = 5.5
	if agg.TotalDurationMs.Avg != 5.5 {
		t.Errorf("M7: Avg want 5.5, got %v", agg.TotalDurationMs.Avg)
	}
	// p50: floor((10-1)*0.50) = floor(4.5) = 4 → sorted[4] = 5
	if agg.TotalDurationMs.P50 != 5 {
		t.Errorf("M7: P50 want 5, got %v", agg.TotalDurationMs.P50)
	}
	// p95: floor((10-1)*0.95) = floor(8.55) = 8 → sorted[8] = 9
	if agg.TotalDurationMs.P95 != 9 {
		t.Errorf("M7: P95 want 9, got %v", agg.TotalDurationMs.P95)
	}
}

// ---------------------------------------------------------------------------
// M8: Concurrent Record from 100 goroutines — Snapshot is consistent.
// Run with go test -race to verify no data race.
// ---------------------------------------------------------------------------

func TestRingRecorder_ConcurrentRecord(t *testing.T) {
	t.Parallel()
	r := NewRingRecorder(200)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Record(Event{
				Timestamp:         time.Now(),
				TotalDurationMs:   int64(n),
				FinalChunksReturned: n % 10,
			})
		}(i)
	}
	wg.Wait()

	snap := r.Snapshot()
	if len(snap) != 100 {
		t.Errorf("M8: expected 100 events in snapshot, got %d", len(snap))
	}
	// All events must be well-formed (no torn reads).
	for i, e := range snap {
		if e.TotalDurationMs < 0 || e.TotalDurationMs >= 100 {
			t.Errorf("M8: snap[%d].TotalDurationMs out of range: %d", i, e.TotalDurationMs)
		}
	}
}
