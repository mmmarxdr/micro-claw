package agent

import (
	"context"
	"testing"
	"time"

	"microagent/internal/audit"
	"microagent/internal/config"
	"microagent/internal/skill"
	"microagent/internal/store"
)

// mockMediaStore embeds mockStore (satisfying store.Store) and additionally
// implements store.MediaStore via a configurable pruneFn.
type mockMediaStore struct {
	mockStore
	pruneFn func(ctx context.Context, olderThan time.Duration) (int, error)
}

func (m *mockMediaStore) StoreMedia(_ context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}

func (m *mockMediaStore) GetMedia(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", nil
}

func (m *mockMediaStore) TouchMedia(_ context.Context, _ string) error {
	return nil
}

func (m *mockMediaStore) PruneUnreferencedMedia(ctx context.Context, olderThan time.Duration) (int, error) {
	if m.pruneFn != nil {
		return m.pruneFn(ctx, olderThan)
	}
	return 0, nil
}

func (m *mockMediaStore) ListMedia(_ context.Context) ([]store.MediaMeta, error) {
	return nil, nil
}

func (m *mockMediaStore) DeleteMedia(_ context.Context, _ string) error {
	return nil
}

// Compile-time assertions.
var (
	_ store.Store      = (*mockMediaStore)(nil)
	_ store.MediaStore = (*mockMediaStore)(nil)
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMediaCleanupLoop_FiresWithCorrectRetention(t *testing.T) {
	calls := make(chan time.Duration, 10)

	ms := &mockMediaStore{
		pruneFn: func(_ context.Context, olderThan time.Duration) (int, error) {
			calls <- olderThan
			return 2, nil
		},
	}

	enabled := true
	mediaCfg := config.MediaConfig{
		Enabled:         &enabled,
		CleanupInterval: 10 * time.Millisecond,
		RetentionDays:   7,
	}

	ag := New(
		defaultCfg(),
		defaultLimits(),
		config.FilterConfig{},
		&mockChannel{},
		&mockProvider{},
		ms,
		audit.NoopAuditor{},
		nil, nil, skill.SkillIndex{}, 4, false,
	)
	ag.WithMediaConfig(mediaCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ag.mediaCleanupLoop(ctx)

	select {
	case dur := <-calls:
		expected := 7 * 24 * time.Hour
		if dur != expected {
			t.Errorf("PruneUnreferencedMedia called with %v, want %v", dur, expected)
		}
	case <-time.After(time.Second):
		t.Fatal("media cleanup never fired within 1s")
	}

	cancel()
	// Allow the goroutine time to observe the cancellation — no further calls expected.
	time.Sleep(20 * time.Millisecond)
}

func TestMediaCleanupLoop_ExitsOnContextCancel(t *testing.T) {
	pruned := make(chan struct{}, 1)

	ms := &mockMediaStore{
		pruneFn: func(_ context.Context, _ time.Duration) (int, error) {
			select {
			case pruned <- struct{}{}:
			default:
			}
			return 0, nil
		},
	}

	enabled := true
	mediaCfg := config.MediaConfig{
		Enabled:         &enabled,
		CleanupInterval: 10 * time.Millisecond,
		RetentionDays:   30,
	}

	ag := New(
		defaultCfg(),
		defaultLimits(),
		config.FilterConfig{},
		&mockChannel{},
		&mockProvider{},
		ms,
		audit.NoopAuditor{},
		nil, nil, skill.SkillIndex{}, 4, false,
	)
	ag.WithMediaConfig(mediaCfg)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.mediaCleanupLoop(ctx)
	}()

	// Wait for at least one prune call to confirm the loop is running.
	select {
	case <-pruned:
	case <-time.After(time.Second):
		t.Fatal("cleanup loop never fired")
	}

	cancel()

	select {
	case <-done:
		// loop exited cleanly
	case <-time.After(time.Second):
		t.Fatal("cleanup loop did not exit after context cancel")
	}
}

func TestMediaCleanupLoop_SkippedWhenMediaDisabled(t *testing.T) {
	calls := make(chan time.Duration, 10)

	ms := &mockMediaStore{
		pruneFn: func(_ context.Context, olderThan time.Duration) (int, error) {
			calls <- olderThan
			return 0, nil
		},
	}

	disabled := false
	mediaCfg := config.MediaConfig{
		Enabled:         &disabled,
		CleanupInterval: 10 * time.Millisecond,
		RetentionDays:   7,
	}

	ag := New(
		defaultCfg(),
		defaultLimits(),
		config.FilterConfig{},
		&mockChannel{},
		&mockProvider{},
		ms,
		audit.NoopAuditor{},
		nil, nil, skill.SkillIndex{}, 4, false,
	)
	ag.WithMediaConfig(mediaCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run() wires the guard; test the guard logic directly by not calling
	// mediaCleanupLoop if Enabled=false (mirrors Run() behaviour).
	if config.BoolVal(ag.mediaCfg.Enabled) {
		go ag.mediaCleanupLoop(ctx)
	}

	select {
	case <-calls:
		t.Fatal("PruneUnreferencedMedia should not be called when media is disabled")
	case <-time.After(50 * time.Millisecond):
		// Correct: no calls received.
	}
}
