package agent

import (
	"context"
	"log/slog"
	"time"

	"microagent/internal/store"
)

// mediaCleanupLoop runs a periodic cleanup of unreferenced media blobs.
// It fires on every cfg.Media.CleanupInterval tick and calls
// store.MediaStore.PruneUnreferencedMedia with the configured retention window.
// The loop exits cleanly when ctx is cancelled.
func (a *Agent) mediaCleanupLoop(ctx context.Context) {
	interval := a.mediaCfg.CleanupInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			retention := time.Duration(a.mediaCfg.RetentionDays) * 24 * time.Hour
			if retention <= 0 {
				retention = 30 * 24 * time.Hour
			}

			ms, ok := a.store.(store.MediaStore)
			if !ok {
				// Store no longer implements MediaStore — should not happen
				// (wiring guard in Run() prevents this). Exit silently.
				return
			}

			deleted, err := ms.PruneUnreferencedMedia(ctx, retention)
			if err != nil {
				slog.Error("media cleanup failed", "error", err)
				continue
			}
			if deleted > 0 {
				slog.Info("media cleanup completed", "deleted_blobs", deleted)
			}
		}
	}
}
