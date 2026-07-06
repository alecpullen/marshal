package mcp

import (
	"context"
	"fmt"
	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

type Manager struct {
	config  *config.Config
	clients []*Client
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		config: cfg,
	}
}

func (m *Manager) Start(ctx context.Context) error {
	if m.config == nil {
		return nil
	}
	for name, srv := range m.config.MCP.Servers {
		var env []string
		for k, v := range srv.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		client := NewClient(name, srv.Command, srv.Args, env)
		if err := client.Start(ctx); err != nil {
			m.Close()
			return fmt.Errorf("start MCP server %q: %w", name, err)
		}
		m.clients = append(m.clients, client)
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
	ctx := context.Background()
	for _, client := range m.clients {
		var res ListToolsResult
		if err := client.Call(ctx, "tools/list", nil, &res); err != nil {
			return fmt.Errorf("list tools from server %s: %w", client.Name, err)
		}

		for _, tool := range res.Tools {
			toolName := fmt.Sprintf("mcp.%s.%s", client.Name, tool.Name)
			err := reg.Register(registry.Tool{
				Name:        toolName,
				Description: tool.Description,
				Schema:      tool.InputSchema,
				Risk:        registry.RiskWorkspaceWrite, // secure default; configurable via policy
				Handler:     m.makeHandler(client, tool.Name),
			})
			if err != nil {
				return fmt.Errorf("register MCP tool %q: %w", toolName, err)
			}
		}
	}
	return nil
}

func (m *Manager) makeHandler(client *Client, mcpToolName string) registry.ToolHandler {
	return func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		params := CallToolParams{
			Name:      mcpToolName,
			Arguments: call.Args,
		}
		var res CallToolResult
		if err := client.Call(ctx, "tools/call", params, &res); err != nil {
			return registry.ToolResult{}, err
		}
		var summary string
		var fullContent string
		if len(res.Content) > 0 {
			summary = res.Content[0].Text
			for _, content := range res.Content {
				if content.Type == "text" {
					fullContent += content.Text + "\n"
				}
			}
		}
		return registry.ToolResult{
			Summary: summary,
			Content: fullContent,
		}, nil
	}
}
