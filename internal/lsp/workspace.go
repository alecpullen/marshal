package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/internal/repo"
)

type clientSource interface {
	ServerFor(lang string) (*Client, bool)
	Root() string
}

type openDoc struct {
	version int
	mtime   time.Time
}

// Workspace keeps a manager's servers in sync with files on disk and turns
// published diagnostics into "errors this edit caused".
type Workspace struct {
	src                   clientSource
	quiet, grace, maxWait time.Duration

	mu       sync.Mutex
	open     map[string]openDoc
	lastScan time.Time
}

func NewWorkspace(src clientSource) *Workspace {
	return &Workspace{src: src, quiet: 300 * time.Millisecond, grace: 1500 * time.Millisecond, maxWait: 3 * time.Second, open: map[string]openDoc{}, lastScan: time.Now()}
}

// FileError is one error diagnostic, 1-based line, path relative to root.
type FileError struct {
	Path    string
	Line    int
	Message string
}

func (w *Workspace) abs(path string) (string, bool) {
	root := w.src.Root()
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return abs, true
}

// DidWrite tells path's server about the bytes now on disk.
func (w *Workspace) DidWrite(path string) error {
	abs, ok := w.abs(path)
	if !ok {
		return fmt.Errorf("lsp: %s is outside the workspace root", path)
	}
	c, ok := w.src.ServerFor(serverLang(abs))
	if !ok {
		return fmt.Errorf("lsp: no ready server for %s", path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	w.mu.Lock()
	doc, isOpen := w.open[abs]
	doc.version++
	doc.mtime = info.ModTime()
	w.open[abs] = doc
	w.mu.Unlock()
	uri := toFileURI("", abs)
	if !isOpen {
		_ = c.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": languageID(abs), "version": doc.version, "text": string(data)}})
	} else {
		_ = c.Notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": doc.version}, "contentChanges": []any{map[string]any{"text": string(data)}}})
	}
	return c.Notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
}

// clientsFor returns the distinct ready clients covering every path, or
// false when any path is outside the root or has no ready server.
func (w *Workspace) clientsFor(paths []string) ([]*Client, bool) {
	seen := map[*Client]bool{}
	var out []*Client
	for _, p := range paths {
		abs, ok := w.abs(p)
		if !ok {
			return nil, false
		}
		c, ok := w.src.ServerFor(serverLang(abs))
		if !ok {
			return nil, false
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, len(out) > 0
}

func snapshot(clients []*Client) map[string][]Diagnostic {
	out := map[string][]Diagnostic{}
	for _, c := range clients {
		for uri, ds := range c.AllDiagnostics() {
			out[uri] = append(out[uri], ds...)
		}
	}
	return out
}

// settle waits for the servers to finish reacting to writes made at since.
//
// The grace period is a floor on every settle, not just the "no publish
// arrived" case. A server republishes the edited file's own diagnostics
// first — usually cleared, because the files that depend on it have not been
// re-analysed yet — and that early publish would otherwise close the quiet
// window long before the error in a dependent file arrives. Waiting out the
// floor is what makes "errors this edit caused, in any file" real.
func (w *Workspace) settle(ctx context.Context, clients []*Client, since time.Time) bool {
	for {
		now := time.Now()
		var latest time.Time
		for _, c := range clients {
			if _, last := c.DiagState(); last.After(latest) {
				latest = last
			}
		}
		switch {
		case latest.After(since) && now.Sub(since) >= w.grace && now.Sub(latest) >= w.quiet:
			return true
		case !latest.After(since) && now.Sub(since) >= w.grace:
			return true
		case now.Sub(since) >= w.maxWait, ctx.Err() != nil:
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// BeginEdit snapshots errors before a write to paths. ok is false when any
// path is outside the root or lacks a ready server; the caller then uses the
// command checker. finish syncs the written files, waits for the servers to
// settle and renders the errors the edit introduced, in any file.
func (w *Workspace) BeginEdit(paths []string) (func(ctx context.Context) string, bool) {
	clients, ok := w.clientsFor(paths)
	if !ok {
		return nil, false
	}
	before := snapshot(clients)
	return func(ctx context.Context) string {
		since := time.Now()
		for _, p := range paths {
			_ = w.DidWrite(p)
		}
		settled := w.settle(ctx, clients, since)
		return RenderNewErrors(w.relative(NewErrors(before, snapshot(clients))), settled)
	}, true
}

func (w *Workspace) relative(errs []FileError) []FileError {
	root := w.src.Root()
	for i := range errs {
		if rel, err := filepath.Rel(root, errs[i].Path); err == nil && !strings.HasPrefix(rel, "..") {
			errs[i].Path = rel
		}
	}
	return errs
}

// NewErrors returns errors in after that before did not have, matching by
// (file, message) as a multiset so a line shift is not a new error.
func NewErrors(before, after map[string][]Diagnostic) []FileError {
	uris := make([]string, 0, len(after))
	for uri := range after {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	var out []FileError
	for _, uri := range uris {
		seen := map[string]int{}
		for _, d := range before[uri] {
			if d.Severity == 1 {
				seen[d.Message]++
			}
		}
		path, err := fromFileURI(uri, "")
		if err != nil {
			path = uri
		}
		for _, d := range after[uri] {
			if d.Severity != 1 {
				continue
			}
			if seen[d.Message] > 0 {
				seen[d.Message]--
				continue
			}
			out = append(out, FileError{Path: path, Line: d.Range.Start.Line + 1, Message: d.Message})
		}
	}
	return out
}

const maxRenderedErrors = 10

// RenderNewErrors formats NewErrors output for a tool result.
func RenderNewErrors(errs []FileError, settled bool) string {
	note := ""
	if !settled {
		note = " (diagnostics still settling)"
	}
	if len(errs) == 0 {
		return "diagnostics: no new errors" + note
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diagnostics: %d new error(s)%s\n", len(errs), note)
	for i, e := range errs {
		if i == maxRenderedErrors {
			files := map[string]bool{}
			for _, r := range errs[i:] {
				files[r.Path] = true
			}
			fmt.Fprintf(&b, "+%d more across %d files\n", len(errs)-i, len(files))
			break
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", e.Path, e.Line, e.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Rescan reports files changed on disk since the last scan (by shell
// commands, git, formatters) to their servers: open documents are re-sent,
// others announced via workspace/didChangeWatchedFiles.
func (w *Workspace) Rescan() {
	root := w.src.Root()
	w.mu.Lock()
	cutoff := w.lastScan
	w.lastScan = time.Now()
	w.mu.Unlock()
	changes := map[*Client][]any{}
	var reopen []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && repo.IsDefaultIgnoredDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		c, ok := w.src.ServerFor(serverLang(path))
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().After(cutoff) {
			return nil
		}
		w.mu.Lock()
		doc, isOpen := w.open[path]
		w.mu.Unlock()
		if isOpen {
			if info.ModTime().After(doc.mtime) {
				reopen = append(reopen, path)
			}
			return nil
		}
		changes[c] = append(changes[c], map[string]any{"uri": toFileURI("", path), "type": 2})
		return nil
	})
	for _, p := range reopen {
		_ = w.DidWrite(p)
	}
	for c, ch := range changes {
		_ = c.Notify("workspace/didChangeWatchedFiles", map[string]any{"changes": ch})
	}
}
