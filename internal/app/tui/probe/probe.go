package probe

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

var Timeout = 5 * time.Second

// IsLocalhost reports whether baseURL points at the local machine.
// Empty input is not local. Shares the routing gate's definition.
func IsLocalhost(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	return routing.IsLocalProvider(baseURL)
}

// Provider probes name's model list. dataDir enables the on-disk limit
// table so probed models carry context/output limits; the cached table is
// refreshed from remote sources only when remoteLimitDiscovery is set.
// Pass "" to skip limit resolution.
func Provider(fieldID, name string, pc config.ProviderConfig, dataDir string, remoteLimitDiscovery bool) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfig(name, pc, dataDir, remoteLimitDiscovery)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		models, err := p.Models(ctx)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		return ResultMsg{FieldID: fieldID, Provider: name, Models: models}
	}
}

type ResultMsg struct {
	FieldID  string
	Provider string
	Models   []schema.ModelInfo
	Err      error
}
