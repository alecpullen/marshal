package provider

import (
	"net/url"
	"strings"
)

// OpenCode Go routes model families across three wire protocols (see
// the endpoint table at https://opencode.ai/docs/go). These constants
// name the request paths; /chat/completions is the status-quo default
// for every model family not listed in the prefix tables below.
const (
	endpointChat      = "/chat/completions"
	endpointResponses = "/responses"
	endpointMessages  = "/messages"
)

// opencodeGoResponsesPrefixes and opencodeGoMessagesPrefixes are
// anchored to the Go docs table (retrieved 2026-09-26). Prefix rules
// tolerate version bumps within a family; if a family ever moves
// endpoints, the misroute fails loudly (an HTTP error from the gateway),
// never silently.
var (
	opencodeGoResponsesPrefixes = []string{"grok-4.", "gpt-", "muse-spark-"}
	opencodeGoMessagesPrefixes  = []string{"minimax-", "qwen3."}
)

// opencodeEndpointFor maps a bare model ID to its Go endpoint path.
// Unknown IDs fall through to /chat/completions — the status-quo
// behavior and the documented endpoint for every family not listed
// above.
func opencodeEndpointFor(model string) string {
	for _, prefix := range opencodeGoResponsesPrefixes {
		if strings.HasPrefix(model, prefix) {
			return endpointResponses
		}
	}
	for _, prefix := range opencodeGoMessagesPrefixes {
		if strings.HasPrefix(model, prefix) {
			return endpointMessages
		}
	}
	return endpointChat
}

// isOpencodeGo reports whether this provider is OpenCode Go — the
// product whose model table routes per-model across /responses,
// /messages, and /chat/completions. Gated on the /zen/go path, NOT the
// bare host: plain Zen (…/zen/v1) shares opencode.ai but routes some of
// the same model IDs differently (e.g. minimax-m3 is /chat/completions
// there but /messages here). A provider named opencode-go matches
// regardless of base URL (the built-in template ID, even when its base
// URL points at a proxy).
func (p *OpenAICompatible) isOpencodeGo() bool {
	if p.name == "opencode-go" {
		return true
	}
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host != "opencode.ai" && !strings.HasSuffix(host, ".opencode.ai") {
		return false
	}
	return strings.HasPrefix(u.Path, "/zen/go")
}
