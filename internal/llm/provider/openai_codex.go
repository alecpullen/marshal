package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"marshal/internal/app/config"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
	"marshal/internal/redact"
)

// Codex wire constants. Every value here is transcribed from the codex CLI
// source tree (openai/codex @ 44fe510c) and confirmed against the live
// endpoint by the spike — see docs/codex-spike-findings-2026-09-14.md §3.
// Do not re-derive these from the article: the article's "whisker" header
// family and its "hardcoded system prompt" claim were both wrong.
const (
	// codexResponsesPath is appended to the provider base URL. The default
	// base URL is https://chatgpt.com/backend-api, giving the pinned
	// endpoint https://chatgpt.com/backend-api/codex/responses.
	codexResponsesPath = "/codex/responses"

	// codexOriginatorHeader and codexOriginator identify the client to the
	// endpoint. Codex sends codex_cli_rs; the endpoint accepts it and does
	// not reject marshal.
	codexOriginatorHeader = "originator"
	codexOriginator       = "codex_cli_rs"

	// codexAccountIDHeader carries the ChatGPT account id from the access
	// token's namespaced claim.
	codexAccountIDHeader = "ChatGPT-Account-ID"

	// codexAuthClaimPath is the namespaced claim holding the account id.
	codexAuthClaimPath = "https://api.openai.com/auth"

	// codexAccountIDClaim is the account-id key inside that claim.
	codexAccountIDClaim = "chatgpt_account_id"

	// codexErrorCodeHeader carries the machine-readable 401 reason, e.g.
	// "token_expired" or "invalid_token".
	codexErrorCodeHeader = "X-Openai-Ide-Error-Code"

	// codexIncludeReasoning is required on every request: the endpoint
	// returns 400 without it (spike §3).
	codexIncludeReasoning = "reasoning.encrypted_content"
)

// OpenAICodex is the ChatGPT-subscription backend. It speaks the Responses
// API (reusing the shared wire codec) against a pinned endpoint, with an
// OAuth bearer token instead of an API key.
//
// The endpoint is streaming-only and stateless: store=false and stream=true
// are hardcoded, matching codex itself.
type OpenAICodex struct {
	name             string
	baseURL          string
	engine           *oauth.Engine
	httpClient       *http.Client
	capabilities     schema.ProviderCapabilities
	limitsTable      *limits.Table
	reasoningSummary bool
	// staticModels is the template's fallback list. Models() prefers the
	// live catalog, then the disk cache, then this.
	staticModels []schema.ModelInfo
	// dataDir is the shared data directory holding the model cache. Empty
	// disables caching (the live fetch and static list still work).
	dataDir string
	// provCfg is the provider's config, used to hash the cache entry so a
	// repointed provider invalidates its cached list.
	provCfg config.ProviderConfig
}

// CodexOptions configures NewOpenAICodex.
type CodexOptions struct {
	Name             string
	BaseURL          string
	Engine           *oauth.Engine
	HTTPClient       *http.Client
	Capabilities     *schema.ProviderCapabilities
	LimitsTable      *limits.Table
	ReasoningSummary bool
	StaticModels     []string
	// DataDir is the shared data directory for the model cache. Empty
	// disables caching.
	DataDir string
	// ProviderConfig is the config this provider was built from, used for
	// cache hashing.
	ProviderConfig config.ProviderConfig
}

// NewOpenAICodex constructs the codex backend. The engine must be non-nil:
// without it there is no way to obtain a token, and the provider would fail
// on every call.
func NewOpenAICodex(opts CodexOptions) (*OpenAICodex, error) {
	if opts.Name == "" {
		return nil, errors.New("openai_codex: name is required")
	}
	if opts.BaseURL == "" {
		return nil, errors.New("openai_codex: base URL is required")
	}
	if opts.Engine == nil {
		return nil, errors.New("openai_codex: OAuth engine is required")
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = defaultHTTPClient()
	}
	caps := DefaultCapabilities()
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	models := make([]schema.ModelInfo, 0, len(opts.StaticModels))
	for _, id := range opts.StaticModels {
		models = append(models, schema.ModelInfo{ID: id, OwnedBy: opts.Name})
	}
	return &OpenAICodex{
		name:             opts.Name,
		baseURL:          strings.TrimRight(opts.BaseURL, "/"),
		engine:           opts.Engine,
		httpClient:       hc,
		capabilities:     caps,
		limitsTable:      opts.LimitsTable,
		reasoningSummary: opts.ReasoningSummary,
		staticModels:     models,
		dataDir:          opts.DataDir,
		provCfg:          opts.ProviderConfig,
	}, nil
}

func (p *OpenAICodex) Name() string { return p.name }

func (p *OpenAICodex) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return p.capabilities
}

// Models returns the model list via the live catalog → disk cache → static
// fallback chain. It never fails: an unreachable catalog degrades to the
// cached or static list rather than breaking the picker.
func (p *OpenAICodex) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return p.resolveModels(ctx), nil
}

// Chat posts a Responses request to the codex endpoint and streams the
// result back as ChatEvents.
//
// Error contract matches the other backends: HTTP-level failures return
// synchronously, in-stream failures arrive as one ChatEventError followed by
// channel close. A 401 is retried once through a forced token refresh before
// being surfaced as *oauth.ErrAuthRequired.
func (p *OpenAICodex) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	token, err := p.engine.TokenSource(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := p.post(ctx, req, token)
	if err != nil {
		return nil, err
	}

	// A 401 on a token the engine still believes is valid means the server
	// disagrees (revoked, or expired early). Force one refresh and retry
	// before telling the user to log in again.
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		token, err = p.engine.ForceRefresh(ctx)
		if err != nil {
			return nil, err
		}
		resp, err = p.post(ctx, req, token)
		if err != nil {
			return nil, err
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.mapError(resp)
	}

	quota := quotaFromHeaders(resp.Header)
	events := make(chan schema.ChatEvent)
	capture := newWireCapture(p.name)
	go p.stream(capture.wrap(resp.Body), capture, events, quota)
	return events, nil
}

// post builds and sends one request. The body is rebuilt per attempt so the
// retry after a forced refresh is byte-identical.
func (p *OpenAICodex) post(ctx context.Context, req schema.ChatRequest, token string) (*http.Response, error) {
	// The endpoint is streaming-only; a caller asking for a non-streaming
	// response still gets an SSE body, so force the flag rather than
	// letting the wire disagree with the transport.
	req.Stream = true

	// Codex rejects system-role input items. Keep the leading harness prompt
	// in instructions and send later runtime guidance as developer messages.
	body, err := buildResponsesRequestBodyWithSystemRole(req, p.reasoningSummary, []string{codexIncludeReasoning}, "developer")
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.name, err)
	}
	writeRequestCapture(p.name, body)

	url := p.baseURL + codexResponsesPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider %q: build chat request: %w", p.name, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set(codexOriginatorHeader, codexOriginator)
	if acct := accountIDFromAccessToken(token); acct != "" {
		httpReq.Header.Set(codexAccountIDHeader, acct)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, &RequestError{Provider: p.name, Op: "chat request failed", Err: err}
	}
	return resp, nil
}

// stream forwards the shared Responses stream, attaching quota state to the
// terminal Done event. The intermediate channel keeps the shared decoder
// unaware of quota entirely.
func (p *OpenAICodex) stream(body io.ReadCloser, capture *wireCapture, out chan<- schema.ChatEvent, quota *schema.QuotaInfo) {
	defer close(out)
	inner := make(chan schema.ChatEvent)
	go streamResponsesEvents(body, capture, inner)
	for ev := range inner {
		if ev.Type == schema.ChatEventDone && quota != nil {
			ev.Quota = quota
		}
		out <- ev
	}
}

// mapError converts a non-2xx response into a typed error.
//
// The codex endpoint uses two different error shapes (spike §3): 400s return
// {"detail":"..."} rather than the Responses-standard {"error":{...}}, and
// 401s return {"error":{...}} plus an X-Openai-Ide-Error-Code header. Both
// are handled, and a 401 becomes *oauth.ErrAuthRequired so the runner can
// offer re-authentication.
func (p *OpenAICodex) mapError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))

	if resp.StatusCode == http.StatusUnauthorized {
		reason := resp.Header.Get(codexErrorCodeHeader)
		if reason == "" {
			reason = "token rejected"
		}
		if text != "" {
			reason += ": " + text
		}
		return &oauth.ErrAuthRequired{ServerName: p.name, Reason: reason}
	}

	// 400-style {"detail": "..."} — surface the detail alone, since the raw
	// JSON wrapper is noise in the transcript.
	var detail struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &detail) == nil && detail.Detail != "" {
		return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: detail.Detail}
	}
	return &ProviderError{Provider: p.name, StatusCode: resp.StatusCode, Body: text}
}

// accountIDFromAccessToken extracts chatgpt_account_id from the access
// token's namespaced claim. The token is a JWT; only the payload is decoded
// (base64url, no signature verification) because the value is used as a
// request header, not as a trust decision.
//
// Returns "" for an opaque token or any malformed input — the caller omits
// the header rather than failing the request.
func accountIDFromAccessToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	auth, ok := claims[codexAuthClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	acct, _ := auth[codexAccountIDClaim].(string)
	if acct == "" {
		return ""
	}
	// The account id is a stable account identifier; mask it in logs and
	// exports the same way the token itself is masked.
	redact.RegisterSecret(acct)
	return acct
}
