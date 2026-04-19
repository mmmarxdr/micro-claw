package cron

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"daimon/internal/channel"
	"daimon/internal/notify"
	"daimon/internal/store"
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
	scheduler          SchedulerIface
	cronStore          store.CronStore
	origSender         OriginalSender
	notifyOnCompletion bool
	bus                notify.Bus
}

// NewCronChannel creates a CronChannel.
// When notifyOnCompletion is true, successful outputs are prefixed with a
// "📋 Scheduled task '<prompt>':" header so the user knows which task ran.
func NewCronChannel(scheduler SchedulerIface, cronStore store.CronStore, origSender OriginalSender, notifyOnCompletion bool) *CronChannel {
	return &CronChannel{
		scheduler:          scheduler,
		cronStore:          cronStore,
		origSender:         origSender,
		notifyOnCompletion: notifyOnCompletion,
	}
}

// WithBus sets the event bus on the CronChannel, enabling cron.job.completed/failed events.
func (c *CronChannel) WithBus(bus notify.Bus) *CronChannel {
	c.bus = bus
	return c
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
//
// Metadata keys understood by Send:
//   - "cron_error": "true" — message is an error notification; always forwarded
//     with an "⚠️ Task '<prompt>' failed:" prefix instead of the completion header.
func (c *CronChannel) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	// Strip "cron:" prefix to get job ID.
	jobID := strings.TrimPrefix(msg.ChannelID, "cron:")
	if jobID == msg.ChannelID {
		return fmt.Errorf("cron channel: unexpected ChannelID format %q (want \"cron:<id>\")", msg.ChannelID)
	}

	isError := msg.Metadata["cron_error"] == "true"

	// Persist result (errors are stored with ErrorMsg set; output is empty for pure error msgs).
	result := store.CronResult{
		ID:    uuid.New().String(),
		JobID: jobID,
		RanAt: time.Now().UTC(),
	}
	if isError {
		result.ErrorMsg = msg.Text
	} else {
		result.Output = msg.Text
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

		if c.bus != nil {
			evType := notify.EventCronJobCompleted
			var errText string
			if isError {
				evType = notify.EventCronJobFailed
				errText = msg.Text
			}
			c.bus.Emit(notify.Event{
				Type:      evType,
				Origin:    notify.OriginCron,
				JobID:     jobID,
				ChannelID: job.ChannelID,
				Text:      msg.Text,
				Error:     errText,
				Timestamp: time.Now(),
			})
		}

		if job.ChannelID != "" {
			text := c.formatForwardText(msg.Text, job.Prompt, isError)
			fwdMsg := channel.OutgoingMessage{
				ChannelID: job.ChannelID,
				Text:      text,
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

// formatForwardText builds the text to forward to the user's originating channel.
// Error messages are always prefixed with a warning header.
// Success messages are prefixed with a scheduled-task header when notifyOnCompletion
// is enabled; otherwise the raw LLM output is forwarded as-is.
func (c *CronChannel) formatForwardText(text, prompt string, isError bool) string {
	short := truncatePrompt(prompt, 50)
	if isError {
		return fmt.Sprintf("⚠️ Task '%s' failed: %s", short, text)
	}
	if c.notifyOnCompletion {
		return fmt.Sprintf("📋 Scheduled task '%s':\n\n%s", short, text)
	}
	return text
}

// truncatePrompt shortens a prompt to at most n runes, appending "..." if truncated.
func truncatePrompt(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// Stop gracefully stops the scheduler and returns nil.
func (c *CronChannel) Stop() error {
	c.scheduler.Stop()
	return nil
}
