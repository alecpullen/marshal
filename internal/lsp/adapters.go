package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/db"
	"marshal/internal/repo"
)

// SymbolAdapter implements index.LSPSymbols.
type SymbolAdapter struct{ m *Manager }

func NewSymbolAdapter(m *Manager) *SymbolAdapter { return &SymbolAdapter{m: m} }

func (a *SymbolAdapter) DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool) {
	client, ok := a.m.ServerFor(lang)
	if !ok {
		return nil, false
	}
	uri := "file://" + filePath
	_ = client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": lang, "version": 1, "text": string(content)},
	})
	raw, err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	_ = client.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	if err != nil {
		return nil, false
	}
	var docs []DocumentSymbol
	if err := jsonUnmarshal(raw, &docs); err != nil || len(docs) == 0 {
		return nil, false
	}
	return MapSymbols(filePath, docs), true
}

// QueryAdapter implements native.LSPQuerier.
type QueryAdapter struct{ m *Manager }

func NewQueryAdapter(m *Manager) *QueryAdapter { return &QueryAdapter{m: m} }

func (a *QueryAdapter) References(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/references", map[string]any{"includeDeclaration": true})
}
func (a *QueryAdapter) Definition(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/definition", nil)
}
func (a *QueryAdapter) Hover(ctx context.Context, filePath string, line, col int) (string, bool) {
	client, ok := a.m.ServerFor(langFor(filePath))
	if !ok {
		return "", false
	}
	raw, err := client.Request(ctx, "textDocument/hover", posParams(filePath, line, col, nil))
	if err != nil {
		return "", false
	}
	var h Hover
	if jsonUnmarshal(raw, &h) != nil {
		return "", false
	}
	return string(h.Contents), true
}

func (a *QueryAdapter) locations(ctx context.Context, filePath string, line, col int, method string, extra map[string]any) ([]string, bool) {
	client, ok := a.m.ServerFor(langFor(filePath))
	if !ok {
		return nil, false
	}
	raw, err := client.Request(ctx, method, posParams(filePath, line, col, extra))
	if err != nil {
		return nil, false
	}
	var locs []Location
	if jsonUnmarshal(raw, &locs) != nil {
		// definition may return a single Location, not an array
		var one Location
		if jsonUnmarshal(raw, &one) == nil && one.URI != "" {
			locs = []Location{one}
		}
	}
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		out = append(out, fmt.Sprintf("%s:%d", strings.TrimPrefix(l.URI, "file://"), l.Range.Start.Line+1))
	}
	return out, true
}

// DiagnosticsAdapter implements diagnostics.LSPSource.
type DiagnosticsAdapter struct{ m *Manager }

func NewDiagnosticsAdapter(m *Manager) *DiagnosticsAdapter { return &DiagnosticsAdapter{m: m} }

func (a *DiagnosticsAdapter) Diagnostics(lang, filePath string) (string, bool) {
	client, ok := a.m.ServerFor(lang)
	if !ok {
		return "", false
	}
	diags := client.Diagnostics("file://" + filePath)
	if len(diags) == 0 {
		return "", true // ready server, no problems
	}
	var b strings.Builder
	for _, d := range diags {
		fmt.Fprintf(&b, "%s:%d: %s\n", filePath, d.Range.Start.Line+1, d.Message)
	}
	return strings.TrimSpace(b.String()), true
}

// ── helpers ──────────────────────────────────────────────────────────────

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

func posParams(filePath string, line, col int, extra map[string]any) map[string]any {
	p := map[string]any{
		"textDocument": map[string]any{"uri": "file://" + filePath},
		"position":     map[string]any{"line": line, "character": col},
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func langFor(filePath string) string {
	return repo.DetectLanguage(filePath)
}
