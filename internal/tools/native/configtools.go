package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/config"
	"marshal/internal/tools/registry"
)

// newConfigToolSet builds a minimal toolSet view for the config tools. It
// copies only the fields the config tools need from the parent toolSet so
// tests can construct one without the full native Options surface.
func newConfigToolSet(t toolSet) (toolSet, error) {
	// Allow tests that set only config (no paths) to proceed; the read
	// tool needs no paths. Write tools check paths at call time.
	return t, nil
}

// configTools returns the full set of config.* tools. Built up across tasks;
// this initial version returns only config.read. Later tasks append the
// section write tools.
func (t *toolSet) configTools() []registry.Tool {
	return []registry.Tool{
		t.configReadTool(),
	}
}

func (t *toolSet) configReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "config.read",
		Description: "Read the current Marshal configuration (merged project + global). Secret fields (api_key, search_key) are masked to \"***\". Use this before changing settings so you know the current values.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"sections":{"type":"array","items":{"type":"string"},"description":"Optional list of top-level section names to include (e.g. [\"agent\",\"swarm\"]). Omit to return all sections."}},"additionalProperties":false}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   true,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		var args struct {
			Sections []string `json:"sections"`
		}
		if len(call.Args) > 0 && string(call.Args) != "" && string(call.Args) != "null" {
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("decode config.read args: %w", err)
			}
		}
		masked := config.MaskSecrets(t.config)
		out, err := filteredConfigJSON(masked, args.Sections)
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{
			Summary: "current configuration (secrets masked)",
			Content: out,
		}, nil
	}
	return tool
}

// filteredConfigJSON marshals cfg to JSON, optionally keeping only the named
// top-level sections. Empty filter returns the whole config.
func filteredConfigJSON(cfg config.Config, sections []string) (string, error) {
	if len(sections) == 0 {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal config: %w", err)
		}
		return string(b), nil
	}
	// Marshal to a map, then keep only requested keys.
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return "", fmt.Errorf("remarshal config: %w", err)
	}
	keep := map[string]any{}
	for _, s := range sections {
		if v, ok := full[s]; ok {
			keep[s] = v
		}
	}
	b, err := json.MarshalIndent(keep, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal filtered config: %w", err)
	}
	return string(b), nil
}
