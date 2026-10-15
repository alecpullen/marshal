package lsp

import (
	"context"

	"marshal/internal/db"
)

// SymbolAdapter implements index.LSPSymbols.
type SymbolAdapter struct{ h *Handle }

func NewSymbolAdapter(h *Handle) *SymbolAdapter { return &SymbolAdapter{h: h} }

func (a *SymbolAdapter) DocumentSymbols(ctx context.Context, lang, filePath string, content []byte) ([]db.Symbol, bool) {
	client, ok := a.h.ServerFor(lang)
	if !ok {
		return nil, false
	}
	uri := toFileURI(a.h.Root(), filePath)
	_ = client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": lang, "version": 1, "text": string(content)},
	})
	raw, err := client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	_ = client.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
	client.ClearDiagnostics(uri)
	if err != nil {
		return nil, false
	}
	var docs []DocumentSymbol
	if err := jsonUnmarshal(raw, &docs); err != nil || len(docs) == 0 {
		return nil, false
	}
	return MapSymbols(filePath, docs), true
}
