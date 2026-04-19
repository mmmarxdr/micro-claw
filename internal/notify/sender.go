package notify

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	"daimon/internal/audit"
	"daimon/internal/channel"
	"daimon/internal/config"
)

// channelSender is a minimal interface for sending outgoing messages.
// It is satisfied by channel.Channel and channel.MultiplexChannel.
type channelSender interface {
	Send(ctx context.Context, msg channel.OutgoingMessage) error
}

// NotificationSender delivers rendered notifications to a channel.
type NotificationSender struct {
	mux     channelSender
	auditor audit.Auditor
	bus     Bus
}

// NewNotificationSender creates a sender that delivers via mux and records audit events.
//   - mux: any channel.Channel implementation (typically *channel.MultiplexChannel).
//   - auditor: records notification.sent / notification.failed audit events.
//   - bus: used to emit EventNotificationSent / EventNotificationFailed with OriginNotification.
func NewNotificationSender(mux channelSender, auditor audit.Auditor, bus Bus) *NotificationSender {
	return &NotificationSender{
		mux:     mux,
		auditor: auditor,
		bus:     bus,
	}
}

// Send delivers a notification for the given rule and event. Thread-safe.
//
//  1. Determines target channel: rule.TargetChannel if set, else event.ChannelID.
//  2. Renders rule.Template with the event as data (text/template). On template error
//     falls back to event.Text.
//  3. Calls mux.Send with the rendered message.
//  4. On failure, if rule.FallbackChannel is set, retries on the fallback.
//  5. Emits an audit event (notification.sent on success, notification.failed on error).
//  6. Emits a bus event with OriginNotification (dropped by the bus worker — loop guard).
//  7. Returns nil on success, or the final error if both primary and fallback fail.
func (s *NotificationSender) Send(ctx context.Context, rule config.NotificationRule, event Event) error {
	start := time.Now()

	// 1. Resolve target channel.
	target := rule.TargetChannel
	if target == "" {
		target = event.ChannelID
	}

	// 2. Render template.
	text := s.renderTemplate(rule, event)

	// 3. Attempt primary send.
	err := s.mux.Send(ctx, channel.OutgoingMessage{
		ChannelID: target,
		Text:      text,
	})

	usedChannel := target
	if err != nil && rule.FallbackChannel != "" {
		// 4. Primary failed — try fallback.
		fbErr := s.mux.Send(ctx, channel.OutgoingMessage{
			ChannelID: rule.FallbackChannel,
			Text:      text,
		})
		if fbErr == nil {
			usedChannel = rule.FallbackChannel
			err = nil
		}
		// If fallback also fails, keep original err (first failure).
	}

	elapsed := time.Since(start)

	// 5. Emit audit event.
	eventType := EventNotificationSent
	toolOK := true
	if err != nil {
		eventType = EventNotificationFailed
		toolOK = false
	}

	details := map[string]string{
		"rule":           rule.Name,
		"target_channel": usedChannel,
		"event_type":     event.Type,
		"job_id":         event.JobID,
	}
	if err != nil {
		details["error"] = err.Error()
	}

	_ = s.auditor.Emit(ctx, audit.AuditEvent{
		ScopeID:    "notify",
		EventType:  eventType,
		Timestamp:  time.Now(),
		DurationMs: elapsed.Milliseconds(),
		ToolName:   "notification_sender",
		ToolOK:     toolOK,
		Details:    details,
	})

	// 6. Emit bus event with OriginNotification (dropped by bus worker — loop guard).
	if s.bus != nil {
		busEventText := fmt.Sprintf("rule=%s channel=%s", rule.Name, usedChannel)
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		s.bus.Emit(Event{
			Type:      eventType,
			Origin:    OriginNotification,
			JobID:     event.JobID,
			ChannelID: usedChannel,
			Text:      busEventText,
			Error:     errStr,
			Timestamp: time.Now(),
		})
	}

	return err
}

// renderTemplate renders rule.Template with the event as data. If the template is
// empty, or if parsing/execution fails, it falls back to event.Text.
func (s *NotificationSender) renderTemplate(rule config.NotificationRule, event Event) string {
	if strings.TrimSpace(rule.Template) == "" {
		return event.Text
	}

	tmpl, err := template.New(rule.Name).Parse(rule.Template)
	if err != nil {
		// Bad template syntax — fall back to raw event text.
		return event.Text
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, event); err != nil {
		// Template execution error — fall back.
		return event.Text
	}

	return buf.String()
}
