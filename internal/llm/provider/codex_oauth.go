package provider

import (
	"fmt"
	"sync"

	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/oauth"
)

// Codex OAuth flow constants. Transcribed from the codex CLI source tree
// (openai/codex @ 44fe510c) and confirmed against the live authorization
// server by the spike — see docs/codex-spike-findings-2026-09-14.md §1.
//
// These are the values that make the codex flow a *pinned* flow: the
// authorization server is known, the client is a fixed public client with no
// secret, and there is no dynamic client registration.
const (
	// codexClientID is the public client_id baked into the codex CLI. It is
	// not a secret (public clients have none) and codex itself ships it as
	// a constant.
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// codexIssuer is the authorization server base URL.
	codexIssuer = "https://auth.openai.com"

	// codexAuthorizeURL and codexTokenURL are the pinned endpoints.
	codexAuthorizeURL = codexIssuer + "/oauth/authorize"
	codexTokenURL     = codexIssuer + "/oauth/token"

	// codexScope is the scope set codex requests.
	codexScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"

	// codexRedirectPort and codexRedirectFallbackPort are the loopback ports
	// codex tries, in order. The redirect path is fixed.
	codexRedirectPort         = 1455
	codexRedirectFallbackPort = 1457
)

// codexExtraAuthorizeParams are the server-specific authorization query
// parameters codex sends. They select the simplified CLI flow and ask for
// organization claims on the id_token.
var codexExtraAuthorizeParams = map[string]string{
	"id_token_add_organizations": "true",
	"codex_cli_simplified_flow":  "true",
	"originator":                 codexOriginator,
}

// codexScopes is codexScope split into the slice the engine expects.
var codexScopes = splitScopes(codexScope)

// splitScopes splits a space-separated scope string.
func splitScopes(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// codexCredentialsNew is a seam for tests: the engine needs an OS keyring
// store, which is unavailable in CI.
var codexCredentialsNew = credentials.New

// codexEngines memoizes one engine per provider entry name.
//
// Sharing matters: the engine's mutex is what serializes a proactive
// background refresh against an inline chat-time refresh. Two engines for the
// same provider would hold two mutexes, so both could refresh at once and the
// loser's rotated refresh token would be overwritten. The provider backend and
// the refresh worker must therefore get the same instance.
var (
	codexEnginesMu sync.Mutex
	codexEngines   = map[string]*oauth.Engine{}
)

// CodexOAuthEngine returns the shared OAuth engine for a provider entry,
// creating it on first use. Both the provider backend and the refresh worker
// call this, so they share one mutex and one token store handle.
func CodexOAuthEngine(name string) (*oauth.Engine, error) {
	codexEnginesMu.Lock()
	defer codexEnginesMu.Unlock()
	if e, ok := codexEngines[name]; ok {
		return e, nil
	}
	e, err := NewCodexOAuthEngine(name)
	if err != nil {
		return nil, err
	}
	codexEngines[name] = e
	return e, nil
}

// NewCodexOAuthEngine builds the OAuth engine for one openai_codex provider
// entry. Tokens are stored one-per-provider-entry under
// marshal:provider:<name>, so two codex entries can hold two different
// ChatGPT logins.
//
// The engine is pinned: no RFC 8414 discovery, no dynamic client
// registration. Open is left nil — the interactive Authorize flow is driven
// from the UI layer, and startup only ever reads stored tokens.
func NewCodexOAuthEngine(name string) (*oauth.Engine, error) {
	store, err := codexCredentialsNew("marshal")
	if err != nil {
		// Never fall back to plaintext. Surface the keychain problem so the
		// user learns why the provider cannot authenticate.
		return nil, fmt.Errorf("OS keychain unavailable — OAuth tokens cannot be stored: %w", err)
	}
	return &oauth.Engine{
		ServerURL:  codexIssuer,
		ClientName: "marshal",
		Store:      store,
		StorageKey: "marshal:provider:" + name,
		Scopes:     codexScopes,
		Flow: oauth.FlowConfig{
			Issuer:               codexIssuer,
			AuthorizeURL:         codexAuthorizeURL,
			TokenURL:             codexTokenURL,
			ClientID:             codexClientID,
			ExtraAuthorizeParams: codexExtraAuthorizeParams,
			RedirectPorts:        []int{codexRedirectPort, codexRedirectFallbackPort},
			RedirectPath:         "/auth/callback",
		},
	}, nil
}

// codexStaticModels returns the fallback model list for a codex provider.
// The template's list is authoritative; a provider configured without a
// template (hand-written config.toml) falls back to the same list so the
// picker is never empty.
func codexStaticModels(pc config.ProviderConfig) []string {
	if tpl, ok := Lookup(pc.Template); ok && len(tpl.Models) > 0 {
		return tpl.Models
	}
	if tpl, ok := Lookup("openai-codex"); ok {
		return tpl.Models
	}
	return nil
}
