package agent

import (
	"context"
	"log/slog"
	"time"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/provider"
	"microagent/internal/skill"
	"microagent/internal/store"
	"microagent/internal/tool"
)

type Agent struct {
	config   config.AgentConfig
	limits   config.LimitsConfig
	channel  channel.Channel
	provider provider.Provider
	store    store.Store
	auditor  audit.Auditor
	tools    map[string]tool.Tool
	skills   []skill.SkillContent
	sem      chan struct{} // concurrency semaphore
}

func New(
	cfg config.AgentConfig,
	limits config.LimitsConfig,
	ch channel.Channel,
	prov provider.Provider,
	st store.Store,
	auditor audit.Auditor,
	tools map[string]tool.Tool,
	skills []skill.SkillContent,
	maxConcurrent int,
) *Agent {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	return &Agent{
		config:   cfg,
		limits:   limits,
		channel:  ch,
		provider: prov,
		store:    st,
		auditor:  auditor,
		tools:    tools,
		skills:   skills,
		sem:      make(chan struct{}, maxConcurrent),
	}
}

func (a *Agent) Run(ctx context.Context) error {
	inbox := make(chan channel.IncomingMessage, 100)

	if err := a.channel.Start(ctx, inbox); err != nil {
		return err
	}

	slog.Info("agent loop started")

	for {
		select {
		case <-ctx.Done():
			// Drain semaphore — wait for in-flight messages to complete.
			for i := 0; i < cap(a.sem); i++ {
				a.sem <- struct{}{}
			}
			return ctx.Err()
		case msg := <-inbox:
			go func(m channel.IncomingMessage) {
				select {
				case a.sem <- struct{}{}:
					defer func() { <-a.sem }()
					a.processMessage(ctx, m)
				case <-time.After(30 * time.Second):
					slog.Warn("agent: message dropped, semaphore timeout",
						"channel_id", m.ChannelID,
						"text_preview", truncate(m.Text, 80))
				}
			}(msg)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (a *Agent) Shutdown() error {
	return a.channel.Stop()
}
