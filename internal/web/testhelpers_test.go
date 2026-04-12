package web

import (
	"microagent/internal/config"
)

// minimalConfig returns a *config.Config suitable for unit tests.
func minimalConfig() *config.Config {
	return &config.Config{
		Agent:    config.AgentConfig{Name: "test-agent"},
		Provider: config.ProviderConfig{Type: "anthropic", Model: "claude-test"},
		Channel:  config.ChannelConfig{Type: "cli"},
		Web:      config.WebConfig{Host: "127.0.0.1", Port: 8080},
	}
}
