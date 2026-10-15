package lsp

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"marshal/internal/db"
)

// diagnosticsFileCap bounds how much of a file's content is read for
// diagnostics, avoiding OOM on large generated files. It was reduced from
// an earlier 2 MiB cap; the file is stat'ed first and read only when under
// the cap, so the read itself stays bounded.
const diagnosticsFileCap = 256 * 1024 // 256 KiB

// readDiagnosticsFile reads a file for diagnostics, returning false when the
// file is unreadable or exceeds diagnosticsFileCap. Oversized files are
// skipped (with a warning) rather than read, so the read itself stays bounded
// by the cap.
func readDiagnosticsFile(path string) ([]byte, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if info.Size() > diagnosticsFileCap {
		// Debug, not Warn: this fires once per checked file and would spam
		// the log on every diagnostics run over a large tree.
		slog.Debug("diagnostics: file exceeds size cap, skipping", "path", path, "size", info.Size(), "cap", diagnosticsFileCap)
		return nil, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return content, true
}

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

// QueryAdapter implements native.LSPQuerier.
type QueryAdapter struct{ h *Handle }

func NewQueryAdapter(h *Handle) *QueryAdapter { return &QueryAdapter{h: h} }

func (a *QueryAdapter) References(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/references", map[string]any{"includeDeclaration": true})
}
func (a *QueryAdapter) Definition(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return a.locations(ctx, filePath, line, col, "textDocument/definition", nil)
}
func (a *QueryAdapter) Hover(ctx context.Context, filePath string, line, col int) (string, bool) {
	client, ok := a.h.ServerFor(langFor(filePath))
	if !ok {
		return "", false
	}
	raw, err := client.Request(ctx, "textDocument/hover", posParams(a.h.Root(), filePath, line, col, nil))
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

func (a *QueryAdapter) locations(ctx context.Context, filePath string, line, col int, method string, extra map[string]any) ([]string, bool) {
	client, ok := a.h.ServerFor(langFor(filePath))
	if !ok {
		return nil, false
	}
	raw, err := client.Request(ctx, method, posParams(a.h.Root(), filePath, line, col, extra))
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
		p, err := fromFileURI(l.URI, a.h.Root())
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d", p, l.Range.Start.Line+1))
	}
	return out, true
}

// DiagnosticsAdapter implements diagnostics.LSPSource.
type DiagnosticsAdapter struct {
	h        *Handle
	versions map[string]int
	vmu      sync.Mutex
}

func NewDiagnosticsAdapter(h *Handle) *DiagnosticsAdapter {
	return &DiagnosticsAdapter{h: h, versions: make(map[string]int)}
}

// nextVersion returns the next document version for the given URI,
// incrementing a per-URI counter. Each didOpen carries an increasing
// version so servers that compare versions treat each open as a fresh
// document.
func (a *DiagnosticsAdapter) nextVersion(uri string) int {
	a.vmu.Lock()
	defer a.vmu.Unlock()
	a.versions[uri]++
	return a.versions[uri]
}

func (a *DiagnosticsAdapter) Diagnostics(lang, filePath string) (string, bool) {
	client, ok := a.h.ServerFor(lang)
	if !ok {
		return "", false
	}
	uri := toFileURI(a.h.Root(), filePath)

	// Read file content, capped at diagnosticsFileCap to avoid OOM on
	// generated files. The file is stat'ed first and only read when it is
	// under the cap, so a plain read is already bounded.
	content, ok := readDiagnosticsFile(filePath)
	if !ok {
		return "", false
	}

	// Open the document so the server publishes diagnostics. Each open
	// carries an increasing version for this URI.
	version := a.nextVersion(uri)
	_ = client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": lang, "version": version, "text": string(content)},
	})
	opened := true
	defer func() {
		if opened {
			_ = client.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
			client.ClearDiagnostics(uri)
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
