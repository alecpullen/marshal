package probe

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

var Timeout = 5 * time.Second

// errNoHost is what a probe returns when it was given no client host: without
// the agent side there is no provider client to ask.
var errNoHost = errors.New("probe: no client host")

// IsLocalhost reports whether baseURL points at the local machine.
// Empty input is not local. Shares the routing gate's definition.
func IsLocalhost(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	return routing.IsLocalProvider(baseURL)
}

// Provider probes host for the models pc's endpoint reports, under Timeout,
// and returns a ResultMsg carrying fieldID and name. A nil host yields a
// ResultMsg whose Err is errNoHost.
func Provider(host acpclient.Host, fieldID, name string, pc config.ProviderConfig) tea.Cmd {
	return func() tea.Msg {
		if host == nil {
			return ResultMsg{FieldID: fieldID, Provider: name, Err: errNoHost}
		}
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		models, err := host.ProbeProvider(ctx, name, pc)
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
