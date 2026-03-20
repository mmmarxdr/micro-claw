package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"microagent/internal/channel"
	"microagent/internal/store"
)

// OriginalSender routes a cron job result back to the user's real channel.
type OriginalSender func(ctx context.Context, msg channel.OutgoingMessage) error

// CronChannel implements channel.Channel for the cron subsystem.
// It owns a SchedulerIface and acts as a bridge between cron job fires and the
// agent's inbox. On Send(), it records the result and forwards to the job's
// originating channel via origSender.
//
// ChannelID format: "cron:<job_id>"
type CronChannel struct {
	scheduler  SchedulerIface
	cronStore  store.CronStore
	origSender OriginalSender
}

// NewCronChannel creates a CronChannel.
func NewCronChannel(scheduler SchedulerIface, cronStore store.CronStore, origSender OriginalSender) *CronChannel {
	return &CronChannel{
		scheduler:  scheduler,
		cronStore:  cronStore,
		origSender: origSender,
	}
}

// Name returns "cron".
func (c *CronChannel) Name() string { return "cron" }

// Start initialises the scheduler (loads jobs from DB, starts ticking).
// Non-blocking: the scheduler runs in background goroutines owned by robfig/cron.
func (c *CronChannel) Start(ctx context.Context, inbox chan<- channel.IncomingMessage) error {
	return c.scheduler.Start(ctx, inbox)
}

// Send receives a completed job response from the agent, saves it to cron_results,
// and calls origSender to deliver the text to the user's original channel.
// ChannelID must be "cron:<job_id>".
func (c *CronChannel) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	// Strip "cron:" prefix to get job ID.
	jobID := strings.TrimPrefix(msg.ChannelID, "cron:")
	if jobID == msg.ChannelID {
		return fmt.Errorf("cron channel: unexpected ChannelID format %q (want \"cron:<id>\")", msg.ChannelID)
	}

	// Persist result.
	result := store.CronResult{
		ID:     uuid.New().String(),
		JobID:  jobID,
		RanAt:  time.Now().UTC(),
		Output: msg.Text,
	}
	if err := c.cronStore.SaveResult(ctx, result); err != nil {
		slog.Warn("cron: failed to save result", "job_id", jobID, "err", err)
		// Continue — best effort; don't fail the send.
	}

	// Forward to original sender if configured.
	if c.origSender != nil {
		job, err := c.cronStore.GetJob(ctx, jobID)
		if err != nil {
			slog.Warn("cron: could not look up job for origSender", "job_id", jobID, "err", err)
			return nil
		}
		if job.ChannelID != "" {
			fwdMsg := channel.OutgoingMessage{
				ChannelID: job.ChannelID,
				Text:      msg.Text,
				Metadata:  msg.Metadata,
			}
			if err := c.origSender(ctx, fwdMsg); err != nil {
				slog.Warn("cron: origSender failed", "job_id", jobID, "target_channel", job.ChannelID, "err", err)
				// Do NOT propagate: result already saved.
			}
		}
	}

	return nil
}

// Stop gracefully stops the scheduler and returns nil.
func (c *CronChannel) Stop() error {
	c.scheduler.Stop()
	return nil
}
