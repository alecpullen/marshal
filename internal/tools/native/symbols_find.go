package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/tools/registry"
)

type symbolsFindArgs struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Limit int    `json:"limit"`
}

var validSymbolKinds = map[string]bool{
	"function": true, "method": true, "type": true, "import": true,
}

func (t *toolSet) symbolsFindTool() registry.Tool {
	tool := registry.Tool{
		Name:        "symbols.find",
		Description: "Find functions, methods, types, and imports in the indexed repository by name and/or kind. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["function","method","type","import"]},"limit":{"type":"integer"}},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, errors.New("database not configured for symbols.find")
		}
		args, err := decodeArgs[symbolsFindArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Kind != "" && !validSymbolKinds[args.Kind] {
			return registry.ToolResult{}, fmt.Errorf("symbols.find kind %q is not one of function, method, type, import", args.Kind)
		}

		symbols, err := t.db.FindSymbols(t.projectID, args.Name, args.Kind, args.Limit)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("find symbols: %w", err)
		}
		if len(symbols) == 0 {
			return registry.ToolResult{
				Summary: "No matching symbols",
				Content: "Run repo.index to build the symbol index first, or adjust your filters.",
			}, nil
		}

		var b strings.Builder
		for _, s := range symbols {
			fmt.Fprintf(&b, "%s %s  %s:%d  %s\n", s.Kind, s.Name, s.FilePath, s.LineStart, s.Signature)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Found %d symbols", len(symbols)),
			Content: b.String(),
		}, nil
	}
	return tool
}
