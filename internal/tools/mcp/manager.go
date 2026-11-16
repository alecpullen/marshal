package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/credentials"
	"marshal/internal/oauth"
	"marshal/internal/redact"
	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/registry"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// caller abstracts the MCP client invocation so tests can inject a stub.
// Both the stdio Client and the Streamable-HTTP HTTPClient satisfy it, so
// everything downstream of Start is transport-agnostic.
type caller interface {
	Call(ctx context.Context, method string, params, result any) error
	ServerName() string
	Close() error
}

// validateServerEnv rejects env entries that could hijack the spawned
// process or leak secrets. The key rules live in envutil so the manager,
// the client (buildChildEnv), and the sandbox share one source. See F-SEC-05.
func validateServerEnv(env map[string]string) error {
	for k, v := range env {
		if envutil.IsDangerousKey(k) || envutil.IsSecretKey(k) {
			return fmt.Errorf("MCP server env: %q is on the deny-list", k)
		}
		if strings.ContainsAny(v, "\n\r\x00") {
			return fmt.Errorf("MCP server env: %q contains a forbidden control character", k)
		}
	}
	return nil
}

// mcpAllowListCommands is the set of command basenames that may be spawned
// by the MCP manager without an explicit Trust flag. See F-SEC-06.
var mcpAllowListCommands = map[string]bool{
	"npx": true, "uvx": true,
	"python": true, "python3": true,
	"node": true, "deno": true, "bun": true,
}

func validateServerCommand(srv config.MCPServerConfig) error {
	if srv.Trust == "unrestricted" {
		return nil
	}
	base := filepath.Base(srv.Command)
	if mcpAllowListCommands[base] {
		return nil
	}
	return fmt.Errorf("MCP server command %q is not in the allow-list; set trust = \"unrestricted\" to override", srv.Command)
}

// mcpServerTimeout is the per-server timeout for tools/list calls.
// It is a var (not const) so tests can override it.
var mcpServerTimeout = 10 * time.Second

// mcpShutdownTimeout bounds how long Client.Close waits for the
// readLoop goroutine to drain. Normally the scanner hits EOF
// immediately after the process is killed and pipes are closed,
// but if an MCP server ignores SIGKILL or the stdout pipe is
// stuck, this prevents Close from blocking indefinitely.
// Package-level so tests can override it.
var mcpShutdownTimeout = 3 * time.Second

// ManagerOption configures a Manager.
type ManagerOption func(*Manager)

// WithManagerLogger sets the logger on a Manager. When nil (or unset), the
// manager uses slog.Default().
func WithManagerLogger(l *slog.Logger) ManagerOption {
	return func(m *Manager) { m.Logger = l }
}

// WithCredentialFactory sets the opener the manager uses to reach the
// credential store that holds remote MCP servers' OAuth tokens. Production
// defaults to credentials.New; the owning runtime may supply an explicitly
// selected opener so a manager exercises the same credential path as the rest
// of the app.
//
// The opener is per-manager, never a package global: two managers started
// concurrently can hold different stores without racing each other, and a
// manager constructed without the option cannot observe another manager's
// store. An explicitly supplied nil opener is recorded as such and fails the
// OAuth construction path — it never falls back to the OS keychain, which
// would silently swap a caller's selected store for the real one.
func WithCredentialFactory(open func(string) (credentials.Store, error)) ManagerOption {
	return func(m *Manager) {
		m.credFactory = open
		m.credFactorySet = true
	}
}

type Manager struct {
	Logger *slog.Logger // nil → slog.Default()

	config  *config.Config
	clients []caller

	// credFactory opens the credential store used for OAuth tokens of
	// remote servers. Nil means credentials.New, unless credFactorySet is
	// true, which records an explicitly supplied (and therefore nil,
	// invalid) opener.
	credFactory    func(string) (credentials.Store, error)
	credFactorySet bool
}

func NewManager(cfg *config.Config, opts ...ManagerOption) *Manager {
	m := &Manager{
		config: cfg,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// openCredentialStore opens the credential store for OAuth tokens, honouring
// the instance opener when one was supplied. Production always falls back to
// credentials.New, which refuses to fall back to plaintext storage when no
// secure backend exists.
func (m *Manager) openCredentialStore() (credentials.Store, error) {
	if m.credFactorySet {
		if m.credFactory == nil {
			return nil, errors.New("mcp: credential factory is nil")
		}
		return m.credFactory("marshal")
	}
	return credentials.New("marshal")
}

// log returns the manager's logger, defaulting to slog.Default() when the
// Logger field is nil.
func (m *Manager) log() *slog.Logger {
	if m.Logger == nil {
		return slog.Default()
	}
	return m.Logger
}

// ServerFailure records one MCP server that could not be started. Start
// collects these instead of aborting: a single bad server must not cost the
// user every other server's tools, nor the agent runtime itself.
type ServerFailure struct {
	Name      string
	Transport string
	Err       error
}

func (f ServerFailure) Error() string {
	if f.Transport == "" {
		return fmt.Sprintf("mcp server %q: %v", f.Name, f.Err)
	}
	return fmt.Sprintf("mcp server %q (%s): %v", f.Name, f.Transport, f.Err)
}

// Start connects every configured MCP server and returns the ones that
// failed. A per-server failure is not fatal: the manager keeps the healthy
// clients, and the caller decides how to surface the rest. There is no
// fatal-error return because no failure mode here is fatal to the manager —
// a config that is entirely broken degrades to "no MCP tools", never to
// "no agent".
func (m *Manager) Start(ctx context.Context) []ServerFailure {
	if m.config == nil {
		return nil
	}
	var failures []ServerFailure
	for name, srv := range m.config.MCP.Servers {
		transport, err := Transport(srv)
		if err != nil {
			failures = append(failures, ServerFailure{Name: name, Err: err})
			m.log().Warn("mcp server skipped", "name", name, "error", err)
			continue
		}
		var client caller
		if transport == "http" {
			client, err = m.startRemote(ctx, name, srv)
		} else {
			client, err = m.startStdio(ctx, name, srv)
		}
		if err != nil {
			// startRemote/startStdio close a half-started client themselves.
			// The manager must not Close() the healthy ones.
			failures = append(failures, ServerFailure{Name: name, Transport: transport, Err: err})
			m.log().Warn("mcp server skipped", "name", name, "transport", transport, "error", err)
			continue
		}
		m.clients = append(m.clients, client)
		m.log().Info("mcp connect", "name", name, "transport", transport)
	}
	// Map iteration order is random; sort so the reported order is stable.
	sort.Slice(failures, func(i, j int) bool { return failures[i].Name < failures[j].Name })
	return failures
}

// startStdio spawns a local MCP server process and completes the initialize
// handshake over its stdio pipes.
func (m *Manager) startStdio(ctx context.Context, name string, srv config.MCPServerConfig) (caller, error) {
	if err := validateServerCommand(srv); err != nil {
		return nil, err
	}
	if srv.Trust == "unrestricted" {
		m.log().Warn("mcp server command accepted with unrestricted trust", "name", name, "command", srv.Command)
	}
	if err := validateServerEnv(srv.Env); err != nil {
		return nil, err
	}
	var env []string
	for k, v := range srv.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	client := NewClient(name, srv.Command, srv.Args, env, WithClientLogger(m.log()))
	if err := client.Start(ctx); err != nil {
		// A half-started client still owns a child process and pipes; close it
		// rather than leaving them to the garbage collector.
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// startRemote connects to a Streamable HTTP MCP endpoint. Header values are
// env-interpolated here — never stored resolved in config — and the resolved
// credential-bearing values are registered with the redactor so they are
// masked in logs and exported transcripts.
func (m *Manager) startRemote(ctx context.Context, name string, srv config.MCPServerConfig) (caller, error) {
	if err := ValidateRemoteServer(srv); err != nil {
		return nil, err
	}
	headers, err := ResolveHeaders(srv.Headers)
	if err != nil {
		return nil, err
	}
	registerHeaderSecrets(headers)

	opts := []HTTPClientOption{WithHTTPClientLogger(m.log())}
	if srv.Auth == "oauth" {
		engine, err := m.oauthEngine(name, srv)
		if err != nil {
			return nil, err
		}
		// TokenSource is consulted once per POST, so a token refreshed after
		// startup is picked up without rebuilding the client. RegisterTools
		// and makeHandler are untouched: every tool call routes through the
		// same client and therefore inherits this source.
		opts = append(opts, WithHTTPOAuth(engine.TokenSource))
	}

	client := NewHTTPClient(name, srv.URL, headers, opts...)
	if err := client.Start(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// oauthEngine builds the OAuth engine for a remote server whose config opts
// into auth = "oauth". The credentials store is opened once per startRemote
// call through the manager's own opener (see WithCredentialFactory) and is
// shared by every request the client makes, so a token refreshed on one call
// is visible to the next. Engine.Open is deliberately left nil: startup only
// reads stored tokens (TokenSource never launches a browser); the interactive
// Authorize flow is driven from the UI layer. CacheDir and LoopbackTimeout are
// left zero so the engine's production defaults apply.
func (m *Manager) oauthEngine(name string, srv config.MCPServerConfig) (*oauth.Engine, error) {
	store, err := m.openCredentialStore()
	if err != nil {
		// Never fall back to plaintext and never retry against the OS
		// keychain: the manager's opener is the only store this engine may
		// use. Surface the problem as a per-server failure so the rest of
		// the agent stays usable.
		return nil, fmt.Errorf("mcp server %q: OS keychain unavailable \u2014 OAuth tokens cannot be stored: %w", name, err)
	}
	return &oauth.Engine{
		ServerURL:  srv.URL,
		ClientName: "marshal",
		Store:      store,
		Logger:     m.log(),
	}, nil
}

// registerHeaderSecrets records credential-bearing header values with the
// redactor. A resolved token has no recognisable sigil, so the pattern passes
// in internal/redact cannot find it; registering it masks it verbatim.
//
// The whole value is registered, and so is the credential that follows a
// scheme prefix: "Bearer <token>" is logged whole in some places and as the
// bare token in others, so registering only the whole value would leave the
// token exposed wherever the prefix is absent. Only the text after the FIRST
// space is registered — registering every whitespace-separated word would
// mask ordinary prose that happens to appear in a phrase-valued header.
func registerHeaderSecrets(headers map[string]string) {
	for k, v := range headers {
		if !isSecretHeader(k) {
			continue
		}
		redact.RegisterSecret(v)
		if _, credential, ok := strings.Cut(v, " "); ok {
			redact.RegisterSecret(strings.TrimSpace(credential))
		}
	}
}

// isSecretHeader reports whether an HTTP header conventionally carries a
// credential. envutil.IsSecretKey covers env-style names; the explicit list
// covers the HTTP names that do not follow that convention — Authorization
// above all.
func isSecretHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-api-key", "api-key", "x-auth-token", "x-access-token":
		return true
	}
	return envutil.IsSecretKey(name)
}

func (m *Manager) Close() error {
	for _, client := range m.clients {
		_ = client.Close()
	}
	m.clients = nil
	return nil
}

func (m *Manager) RegisterTools(reg *registry.Registry) error {
	threshold := 0
	if m.config != nil {
		threshold = m.config.MCP.DisclosureThresholdTools
	}

	type pendingTool struct {
		name        string
		description string
		schema      []byte
		client      caller
		mcpToolName string
	}
	var pending []pendingTool

	for _, client := range m.clients {
		srvCtx, cancel := context.WithTimeout(context.Background(), mcpServerTimeout)
		var res ListToolsResult
		if err := client.Call(srvCtx, "tools/list", nil, &res); err != nil {
			cancel()
			m.log().Warn("mcp: server skipped",
				"server", client.ServerName(),
				"error", err,
			)
			continue
		}
		cancel()
		for _, tool := range res.Tools {
			pending = append(pending, pendingTool{
				name:        fmt.Sprintf("mcp.%s.%s", client.ServerName(), tool.Name),
				description: tool.Description,
				schema:      tool.InputSchema,
				client:      client,
				mcpToolName: tool.Name,
			})
		}
	}

	deferred := threshold > 0 && len(pending) > threshold

	for i := range pending {
		p := pending[i]
		if err := reg.Register(registry.Tool{
			Name:        p.name,
			Description: p.description,
			Schema:      p.schema,
			Risk:        registry.RiskWorkspaceWrite, // secure default; configurable via policy
			Deferred:    deferred,
			Handler:     m.makeHandler(p.client, p.client.ServerName(), p.mcpToolName),
		}); err != nil {
			// A third-party server may advertise a schema we cannot compile.
			// Skip that one tool rather than losing every other tool from
			// this server and the ones after it.
			m.log().Warn("mcp: tool skipped",
				"server", p.client.ServerName(),
				"tool", p.mcpToolName,
				"error", err,
			)
			continue
		}
	}
	return nil
}

// The built-in Marshal Studio client.
//
// A session that enrolled on the daemon it runs on is bound to the project the
// daemon already tracks for it, and presents that binding to the factory's own
// tool surface. The client is instance-scoped: it is built here, it is never a
// user MCP config entry, and its credential, its transport and its identity
// belong to this session alone.
const (
	// MarshalStudioServerName is the server name the built-in client registers
	// under, so its tools are addressed as "mcp.marshal-studio.<tool>" exactly
	// as a configured server's are. The name is fixed: a session cannot choose
	// to look like another server, and the bridge's built-in identity is the
	// credential's, not this string's.
	MarshalStudioServerName = "marshal-studio"
	// MarshalStudioEndpoint is the built-in client's URL. The host is a
	// placeholder: the caller's transport dials the daemon's socket, so nothing
	// here is resolved, and the endpoint is never a network address.
	MarshalStudioEndpoint = "http://daemon/mcp"
)

var (
	// ErrMarshalStudioUnauthorized is a built-in call the instance refused: the
	// credential lapsed, the generation moved on, or the operation is outside
	// the access this session holds.
	ErrMarshalStudioUnauthorized = errors.New("marshal studio: not authorized")
	// ErrMarshalStudioUnavailable is a built-in call nothing answered: the
	// instance is gone, or the session is no longer enrolled.
	ErrMarshalStudioUnavailable = errors.New("marshal studio: unavailable")
	// ErrMarshalStudioUnknown is a built-in tool the instance does not
	// advertise — a capability that was never granted rather than one that was
	// lost.
	ErrMarshalStudioUnknown = errors.New("marshal studio: unknown tool")
)

// MarshalStudioOptions is one session's built-in client: where it dials, what it
// presents, and the hooks its recovery rules use. Every field is supplied by the
// runtime that holds the enrollment; nothing here is read from a user's config
// and nothing here is written to one.
type MarshalStudioOptions struct {
	// URL is the endpoint. Empty means MarshalStudioEndpoint.
	URL string
	// HTTPClient sends the requests. It is how the client reaches the daemon's
	// socket; nil leaves the default transport, which cannot reach a socket.
	HTTPClient *http.Client
	// TokenSource supplies the enrollment credential once per request, so a
	// revalidated credential is picked up without rebuilding the client.
	TokenSource func(ctx context.Context) (string, error)
	// RequestHeaders yields extra headers for one POST. Optional.
	RequestHeaders func(ctx context.Context) map[string]string
	// UserDirectionHeader and ConfirmUserDirection admit one operation the
	// instance refused for want of a person's direction. The header name comes
	// from the caller, and the confirmer is the session's ordinary approval
	// path.
	UserDirectionHeader  string
	ConfirmUserDirection func(ctx context.Context, challenge UserDirectionChallenge) (string, error)
	// Revalidate re-runs discovery and enrollment after a 401 or a transport
	// failure. It is consulted at most once per call.
	Revalidate func(ctx context.Context) error
	// Timeout bounds the initialize handshake. Zero means mcpServerTimeout.
	Timeout time.Duration
}

// AttachMarshalStudio connects this session's built-in Studio client and keeps
// it, so RegisterTools discovers its tools through the same tools/list path a
// configured server takes — names, schemas and bounded results are the
// instance's, never hand-written here.
//
// The error is the caller's to record as a status: no enrollment, no socket and
// an instance that refuses all leave the session exactly as it was, with no
// built-in tools and every other tool intact. Nothing is added to a client list
// unless the handshake succeeded, so a failed attach registers nothing.
func (m *Manager) AttachMarshalStudio(ctx context.Context, opts MarshalStudioOptions) error {
	if m == nil {
		return fmt.Errorf("%w: no manager", ErrMarshalStudioUnavailable)
	}
	if opts.TokenSource == nil {
		return fmt.Errorf("%w: this session holds no enrollment", ErrMarshalStudioUnauthorized)
	}
	url := strings.TrimSpace(opts.URL)
	if url == "" {
		url = MarshalStudioEndpoint
	}
	clientOpts := []HTTPClientOption{
		WithHTTPClientLogger(m.log()),
		WithHTTPOAuth(opts.TokenSource),
	}
	if opts.HTTPClient != nil {
		clientOpts = append(clientOpts, WithHTTPClient(opts.HTTPClient))
	}
	if opts.RequestHeaders != nil {
		clientOpts = append(clientOpts, WithHTTPRequestHeaders(opts.RequestHeaders))
	}
	if opts.Revalidate != nil {
		clientOpts = append(clientOpts, WithHTTPRevalidator(opts.Revalidate))
	}
	if opts.ConfirmUserDirection != nil {
		clientOpts = append(clientOpts, WithUserDirectionConfirm(opts.UserDirectionHeader, opts.ConfirmUserDirection))
	}

	client := NewHTTPClient(MarshalStudioServerName, url, nil, clientOpts...)
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = mcpServerTimeout
	}
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := client.Start(startCtx); err != nil {
		// A client that never completed its handshake owns nothing worth
		// keeping, and registering its tools would advertise a surface this
		// session cannot use.
		_ = client.Close()
		return marshalStudioError(err)
	}
	m.clients = append(m.clients, client)
	m.log().Info("mcp connect", "name", MarshalStudioServerName, "transport", "http")
	return nil
}

// marshalStudioError labels a built-in failure with the cause a caller acts on.
// The instance's own words stay in the message; the class is what a model and a
// transcript can reason about.
func marshalStudioError(err error) error {
	if err == nil {
		return nil
	}
	var auth *oauth.ErrAuthRequired
	if errors.As(err, &auth) {
		return fmt.Errorf("%w: %v", ErrMarshalStudioUnauthorized, err)
	}
	var transport *url.Error
	if errors.As(err, &transport) {
		return fmt.Errorf("%w: %v", ErrMarshalStudioUnavailable, err)
	}
	var rpcErr *MCPError
	if errors.As(err, &rpcErr) && rpcErr.Code == mcpMethodNotFoundCode {
		return fmt.Errorf("%w: %v", ErrMarshalStudioUnknown, err)
	}
	return err
}

// marshalStudioFailure labels a built-in tool's failure payload with the cause a
// caller acts on: not authorized, unavailable, or unknown. The factory's own
// words stay in the message; the class is what a model and a transcript can
// reason about without parsing text.
func marshalStudioFailure(payload string) error {
	trimmed := strings.TrimSpace(payload)
	var refusal struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &refusal); err != nil {
		return fmt.Errorf("marshal studio: %s", trimmed)
	}
	reason := strings.TrimSpace(refusal.Message)
	if reason == "" {
		reason = strings.TrimSpace(refusal.Detail)
	}
	if reason == "" {
		reason = refusal.Code
	}
	switch refusal.Code {
	case "denied", "unauthorized", "forbidden":
		return fmt.Errorf("%w: %s", ErrMarshalStudioUnauthorized, reason)
	case userDirectionRequiredCode:
		// The transport asks the person and retries; reaching here means the
		// confirmation did not happen, so the operation was not directed.
		return fmt.Errorf("%w: this operation needs the user's direction and was not directed: %s", ErrMarshalStudioUnauthorized, reason)
	case "unavailable":
		return fmt.Errorf("%w: %s", ErrMarshalStudioUnavailable, reason)
	case "not_found", "unsupported":
		return fmt.Errorf("%w: %s", ErrMarshalStudioUnknown, reason)
	default:
		return fmt.Errorf("marshal studio: %s", reason)
	}
}

// mcpMethodNotFoundCode is the JSON-RPC code an instance uses for a method or
// tool it does not serve.
const mcpMethodNotFoundCode = -32601

func (m *Manager) makeHandler(c caller, serverName, mcpToolName string) registry.ToolHandler {
	return func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		start := time.Now()
		params := CallToolParams{
			Name:      mcpToolName,
			Arguments: call.Args,
		}
		var res CallToolResult
		err := c.Call(ctx, "tools/call", params, &res)
		m.log().Info("mcp call",
			"server", serverName,
			"tool", mcpToolName,
			"duration_ms", time.Since(start).Milliseconds(),
			"error", err,
		)
		if err != nil {
			if serverName == MarshalStudioServerName {
				return registry.ToolResult{}, marshalStudioError(err)
			}
			return registry.ToolResult{}, err
		}
		var summary string
		var fullContent string
		for _, content := range res.Content {
			if content.Type == "text" {
				if summary == "" {
					summary = content.Text
				}
				fullContent += content.Text + "\n"
			}
		}
		if res.IsError {
			summary := summary
			toolResult := registry.ToolResult{
				Summary: summary,
				Content: fullContent,
				Error:   "MCP tool reported error: " + summary,
			}
			if serverName == MarshalStudioServerName {
				failure := marshalStudioFailure(summary)
				toolResult.Error = failure.Error()
				return toolResult, failure
			}
			return toolResult, errors.New("mcp: tool reported error")
		}
		return registry.ToolResult{
			Summary: summary,
			Content: fullContent,
		}, nil
	}
}
