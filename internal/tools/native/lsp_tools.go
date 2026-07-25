package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// LSPQuerier provides LSP-based queries for a file at a given (line, col)
// position. Each method returns the rendered result and a bool indicating
// whether the server is ready.
type LSPQuerier interface {
	Definition(ctx context.Context, filePath string, line, col int) ([]string, bool)
	References(ctx context.Context, filePath string, line, col int) ([]string, bool)
	Hover(ctx context.Context, filePath string, line, col int) (string, bool)
}

type symbolQueryArgs struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
}

// resolveSymbol finds the single matching symbol, or returns a ToolResult
// describing the miss (not-found / ambiguous) with ok=false.
func (t *toolSet) resolveSymbol(name, path string) (db.Symbol, registry.ToolResult, bool) {
	matches, err := t.db.FindSymbols(t.projectID, name, "", 50)
	if err != nil {
		return db.Symbol{}, registry.ToolResult{}, false
	}
	var filtered []db.Symbol
	for _, s := range matches {
		if s.Name != name {
			continue
		}
		if path != "" && s.FilePath != path {
			continue
		}
		filtered = append(filtered, s)
	}
	switch {
	case len(filtered) == 0:
		return db.Symbol{}, registry.ToolResult{Summary: "not found",
			Content: fmt.Sprintf("no indexed symbol named %q", name)}, false
	case len(filtered) > 1:
		var b strings.Builder
		b.WriteString("ambiguous symbol; pass `path` to disambiguate:\n")
		for _, s := range filtered {
			fmt.Fprintf(&b, "  %s:%d\n", s.FilePath, s.LineStart)
		}
		return db.Symbol{}, registry.ToolResult{Summary: "ambiguous", Content: b.String()}, false
	default:
		return filtered[0], registry.ToolResult{}, true
	}
}

func symbolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"symbol":{"type":"string"},"path":{"type":"string"}},"required":["symbol"],"additionalProperties":false}`)
}

func (t *toolSet) lspLocationsTool(name, desc string, call func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool)) registry.Tool {
	tool := registry.Tool{Name: name, Description: desc, Schema: symbolSchema(), Risk: registry.RiskReadOnly}
	tool.Handler = func(ctx context.Context, tc registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolQueryArgs](tool, tc.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		sym, miss, ok := t.resolveSymbol(args.Symbol, args.Path)
		if !ok {
			return miss, nil
		}
		if t.lsp == nil {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		locs, ready := call(t.lsp, ctx, sym.FilePath, sym.LineStart-1)
		if !ready {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		if len(locs) == 0 {
			return registry.ToolResult{Summary: "none", Content: "no results"}, nil
		}
		return registry.ToolResult{Summary: fmt.Sprintf("%d results", len(locs)), Content: strings.Join(locs, "\n")}, nil
	}
	return tool
}

func (t *toolSet) referencesTool() registry.Tool {
	return t.lspLocationsTool("references", "Find all references to a symbol (by name).",
		func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool) {
			return q.References(ctx, path, line, 0)
		})
}

func (t *toolSet) definitionTool() registry.Tool {
	return t.lspLocationsTool("definition", "Find where a symbol is defined (by name).",
		func(q LSPQuerier, ctx context.Context, path string, line int) ([]string, bool) {
			return q.Definition(ctx, path, line, 0)
		})
}

func (t *toolSet) hoverTool() registry.Tool {
	tool := registry.Tool{Name: "hover", Description: "Show a symbol's type signature and documentation (by name).", Schema: symbolSchema(), Risk: registry.RiskReadOnly}
	tool.Handler = func(ctx context.Context, tc registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolQueryArgs](tool, tc.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		sym, miss, ok := t.resolveSymbol(args.Symbol, args.Path)
		if !ok {
			return miss, nil
		}
		if t.lsp == nil {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		text, ready := t.lsp.Hover(ctx, sym.FilePath, sym.LineStart-1, 0)
		if !ready {
			return registry.ToolResult{Summary: "no lsp", Content: "no language server available for this symbol"}, nil
		}
		return registry.ToolResult{Summary: "hover", Content: text}, nil
	}
	return tool
}
