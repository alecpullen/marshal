package probe

import (
	"context"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider"
)

var Timeout = 5 * time.Second

func IsLocalhost(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return true
	}
	return strings.HasPrefix(host, "::1%")
}

func Provider(fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfig(name, pc)
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
