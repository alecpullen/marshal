package probe

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
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

func Provider(fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfig(name, pc, "", false)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		models, err := p.Models(ctx)
		if err != nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: err}
		}
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		return ResultMsg{FieldID: fieldID, Provider: name, Models: ids}
	}
}

type ResultMsg struct {
	FieldID  string
	Provider string
	Models   []string
	Err      error
}
