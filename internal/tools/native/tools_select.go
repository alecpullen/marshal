package native

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"marshal/internal/tools/registry"
)

type loadedToolsStore interface {
	AddLoadedToolNames([]string)
}

type toolsSelectArgs struct {
	Names []string `json:"names"`
}

func (t *toolSet) toolsSelectTool() registry.Tool {
	tool := registry.Tool{
		Name:        "tools.select",
		Description: "Load the schemas for the named deferred MCP tools so they become callable for the rest of the session.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"}}},"required":["names"]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[toolsSelectArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if t.registry == nil {
			return registry.ToolResult{}, fmt.Errorf("tool registry not available")
		}
		if t.sessionState == nil {
			return registry.ToolResult{}, fmt.Errorf("session state not available")
		}
		store, ok := t.sessionState.(loadedToolsStore)
		if !ok {
			return registry.ToolResult{}, fmt.Errorf("session state does not support loaded tools")
		}

		var unknown []string
		var found []string
		for _, name := range args.Names {
			registered, exists := t.registry.Lookup(name)
			if !exists || !registered.Deferred {
				unknown = append(unknown, name)
				continue
			}
			found = append(found, name)
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return registry.ToolResult{}, fmt.Errorf("unknown or non-deferred tools: %s", strings.Join(unknown, ", "))
		}
		store.AddLoadedToolNames(found)
		return registry.ToolResult{
			Summary: "tools selected",
			Content: fmt.Sprintf("loaded: %v", found),
		}, nil
	}
	return tool
}
