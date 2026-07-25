package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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
	text := flattenHoverContents(h.Contents)
	if text == "" {
		return "", false
	}
	return text, true
}

// flattenHoverContents converts the Hover.Contents union (MarkupContent |
// MarkedString | MarkedString[] | string) into a single readable string.
func flattenHoverContents(raw json.RawMessage) string {
	// Try MarkupContent first (most common).
	var mc MarkupContent
	if jsonUnmarshal(raw, &mc) == nil && mc.Value != "" {
		return mc.Value
	}
	// Try MarkedString (object form).
	var ms MarkedString
	if jsonUnmarshal(raw, &ms) == nil && ms.Value != "" {
		return ms.Value
	}
	// Try MarkedString array.
	var msa []MarkedString
	if jsonUnmarshal(raw, &msa) == nil && len(msa) > 0 {
		var b strings.Builder
		for i, m := range msa {
			if i > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(m.Value)
		}
		return b.String()
	}
	// Try bare string.
	var s string
	if jsonUnmarshal(raw, &s) == nil && s != "" {
		return s
	}
	return ""
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
	uri := "file://" + filePath

	// Read file content (cap at 2 MiB to avoid OOM on generated files).
	info, err := os.Stat(filePath)
	if err != nil {
		return "", false
	}
	if info.Size() > 2<<20 { // 2 MiB
		return "", false
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", false
	}

	// Open the document so the server publishes diagnostics.
	_ = client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": lang, "version": 1, "text": string(content)},
	})
	opened := true
	defer func() {
		if opened {
			_ = client.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
		}
	}()

	// Trigger a no-op query to force the server to evaluate the document.
	// We use documentSymbol because it is cheap and universally supported.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = client.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	cancel()

	// Poll for publishDiagnostics with a short deadline (250ms total, 25ms tick).
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		diags := client.Diagnostics(uri)
		if len(diags) > 0 {
			var b strings.Builder
			for _, d := range diags {
				fmt.Fprintf(&b, "%s:%d: %s\n", filePath, d.Range.Start.Line+1, d.Message)
			}
			return strings.TrimSpace(b.String()), true
		}
		time.Sleep(25 * time.Millisecond)
	}

	// No diagnostics published — fall back to commands.
	return "", false
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
