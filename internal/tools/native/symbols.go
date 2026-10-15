package native

import (
	"context"
	"encoding/json"

	"marshal/internal/lsp"
	"marshal/internal/tools/registry"
)

// SymbolQuerier answers symbol lookups (lsp.SymbolQuery: language server
// first, symbol index as fallback).
type SymbolQuerier interface {
	Query(ctx context.Context, req lsp.SymbolRequest) (lsp.SymbolResult, error)
}

type symbolsArgs struct {
	Name string `json:"name"`
	Op   string `json:"op"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func (t *toolSet) symbolsTool() registry.Tool {
	tool := registry.Tool{
		Name: "symbols",
		Description: "Look up code symbols through the language server (falling back to the symbol index). " +
			"op: find (search by name), definition (where it is declared, with an excerpt; the default), references (where it is used), hover (signature and docs). " +
			"name may be qualified as Type.Method. Pass path, or path and line from earlier output, to pin one occurrence.",
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"op":{"type":"string","enum":["find","definition","references","hover"]},"path":{"type":"string"},"line":{"type":"integer"}},"required":["name"],"additionalProperties":false}`),
		Risk:   registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[symbolsArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		res, err := t.symbols.Query(ctx, lsp.SymbolRequest{Op: args.Op, Name: args.Name, Path: args.Path, Line: args.Line})
		if err != nil {
			return registry.ToolResult{}, err
		}
		return registry.ToolResult{Summary: res.Summary, Content: res.Content}, nil
	}
	return tool
}
