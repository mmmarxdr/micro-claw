package agent

import (
	"context"
	"log/slog"

	"microagent/internal/audit"
	"microagent/internal/channel"
	"microagent/internal/config"
	"microagent/internal/provider"
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
}

func New(
	cfg config.AgentConfig,
	limits config.LimitsConfig,
	ch channel.Channel,
	prov provider.Provider,
	st store.Store,
	auditor audit.Auditor,
	tools map[string]tool.Tool,
) *Agent {
	return &Agent{
		config:   cfg,
		limits:   limits,
		channel:  ch,
		provider: prov,
		store:    st,
		auditor:  auditor,
		tools:    tools,
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
			return ctx.Err()
		case msg := <-inbox:
			a.processMessage(ctx, msg)
		}
	}
}

func (a *Agent) Shutdown() error {
	return a.channel.Stop()
}
