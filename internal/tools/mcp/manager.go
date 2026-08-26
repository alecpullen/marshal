package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"marshal/internal/app/config"
	"marshal/internal/sandbox/envutil"
	"marshal/internal/tools/registry"
	"path/filepath"
	"strings"
	"time"
)

// caller abstracts the MCP client invocation so tests can inject a stub.
type caller interface {
	Call(ctx context.Context, method string, params, result any) error
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
	clients []*Client
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
		if err := validateServerCommand(srv); err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		if srv.Trust == "unrestricted" {
			m.log().Warn("mcp server command accepted with unrestricted trust", "name", name, "command", srv.Command)
		}
		if err := validateServerEnv(srv.Env); err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		var env []string
		for k, v := range srv.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		client := NewClient(name, srv.Command, srv.Args, env, WithClientLogger(m.log()))
		if err := client.Start(ctx); err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		m.clients = append(m.clients, client)
		m.log().Info("mcp connect", "name", name)
	}
	return nil
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
		client      *Client
		mcpToolName string
	}
	var pending []pendingTool

	for _, client := range m.clients {
		srvCtx, cancel := context.WithTimeout(context.Background(), mcpServerTimeout)
		var res ListToolsResult
		if err := client.Call(srvCtx, "tools/list", nil, &res); err != nil {
			cancel()
			m.log().Warn("mcp: server skipped",
				"server", client.Name,
				"error", err,
			)
			continue
		}
		cancel()
		for _, tool := range res.Tools {
			pending = append(pending, pendingTool{
				name:        fmt.Sprintf("mcp.%s.%s", client.Name, tool.Name),
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
			Handler:     m.makeHandler(p.client, p.client.Name, p.mcpToolName),
		}); err != nil {
			// A third-party server may advertise a schema we cannot compile.
			// Skip that one tool rather than losing every other tool from
			// this server and the ones after it.
			m.log().Warn("mcp: tool skipped",
				"server", p.client.Name,
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
