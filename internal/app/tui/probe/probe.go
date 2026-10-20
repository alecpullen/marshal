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

// Provider probes name's model list using the production credential/OAuth
// dependency. It is the wrapper for callers that have no injected resolver
// of their own; callers wired from a runtime must use ProviderWithAuth so a
// probe cannot reach a credential backend the runtime did not select.
//
// dataDir enables the on-disk model cache. thinkingBudgetMargin is the
// AgentConfig value threaded to Anthropic providers.
func Provider(fieldID, name string, pc config.ProviderConfig, dataDir string, thinkingBudgetMargin int) tea.Cmd {
	return ProviderWithAuth(fieldID, name, pc, dataDir, thinkingBudgetMargin, provider.DefaultAuthResolver())
}

// ProviderWithAuth probes name's model list through the explicitly supplied
// credential/OAuth dependency. An openai_codex provider builds its token
// store and OAuth engine from auth, so a TUI running under an isolated
// runtime (tests, an embedded app) never falls back to the real OS keychain.
//
// A nil auth fails every probe with an error rather than selecting the
// production adapter: silently substituting the real keychain is exactly
// what an injected resolver exists to prevent.
func ProviderWithAuth(fieldID, name string, pc config.ProviderConfig, dataDir string, thinkingBudgetMargin int, auth provider.AuthResolver) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.NewFromConfigWithAuth(name, pc, dataDir, thinkingBudgetMargin, "", auth)
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
