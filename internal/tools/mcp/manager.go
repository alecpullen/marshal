package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/redact"
	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/registry"
	"path/filepath"
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

type Manager struct {
	Logger *slog.Logger // nil → slog.Default()

	config  *config.Config
	clients []caller
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

// log returns the manager's logger, defaulting to slog.Default() when the
// Logger field is nil.
func (m *Manager) log() *slog.Logger {
	if m.Logger == nil {
		return slog.Default()
	}
	return m.Logger
}

func (m *Manager) Start(ctx context.Context) error {
	if m.config == nil {
		return nil
	}
	for name, srv := range m.config.MCP.Servers {
		transport, err := Transport(srv)
		if err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		var client caller
		if transport == "http" {
			client, err = m.startRemote(ctx, name, srv)
		} else {
			client, err = m.startStdio(ctx, name, srv)
		}
		if err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		m.clients = append(m.clients, client)
		m.log().Info("mcp connect", "name", name, "transport", transport)
	}
	return nil
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
	client := NewHTTPClient(name, srv.URL, headers, WithHTTPClientLogger(m.log()))
	if err := client.Start(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// registerHeaderSecrets records credential-bearing header values with the
// redactor. A resolved token has no recognisable sigil, so the pattern passes
// in internal/redact cannot find it; registering it masks it verbatim.
//
// Both the whole value and its individual words are registered: a header like
// "Bearer <token>" is logged whole in some places and as the bare token in
// others, and only registering the whole value would leave the token exposed
// wherever the scheme prefix is absent. redact.RegisterSecret ignores values
// below its minimum length, so short scheme words are not registered.
func registerHeaderSecrets(headers map[string]string) {
	for k, v := range headers {
		if !isSecretHeader(k) {
			continue
		}
		redact.RegisterSecret(v)
		for _, word := range strings.Fields(v) {
			redact.RegisterSecret(word)
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
			return registry.ToolResult{
				Summary: summary,
				Content: fullContent,
				Error:   "MCP tool reported error: " + summary,
			}, errors.New("mcp: tool reported error")
		}
		return registry.ToolResult{
			Summary: summary,
			Content: fullContent,
		}, nil
	}
}
