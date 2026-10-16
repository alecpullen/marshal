package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"marshal/internal/db"
)

// SymbolRequest is one symbols lookup. Op is find, definition, references or
// hover (empty means definition). Name is the symbol, optionally qualified as
// Type.Method. Path, and Line (1-based) with it, address one occurrence
// directly.
type SymbolRequest struct {
	Op   string
	Name string
	Path string
	Line int
}

// SymbolResult is a rendered answer: Summary is the short label for the tool
// row, Content is the text the model reads.
type SymbolResult struct {
	Summary string
	Content string
}

// SymbolIndex is the slice of the symbol index SymbolQuery falls back to when
// no server can answer. *db.DB satisfies it.
type SymbolIndex interface {
	FindSymbols(projectID int64, name, kind string, limit int) ([]db.Symbol, error)
}

// Durations and output caps, from the spec.
const (
	// symbolWait is how long a query lets a server start before turning to
	// the index. symbolRequestTimeout bounds one request to a server.
	symbolWait           = 10 * time.Second
	symbolRequestTimeout = 10 * time.Second

	maxFindLines       = 20
	maxReferences      = 50
	maxHoverChars      = 1500
	maxNearMatches     = 5
	maxDefinitionLocs  = 5
	maxDefinitionLines = 5

	// indexLookupLimit bounds one index lookup while resolving a name: the
	// rows it returns double as the near-match list.
	indexLookupLimit = 50
)

// SymbolQuery answers symbol lookups through the language servers and falls
// back to Marshal's symbol index wherever no server can answer. It backs the
// `symbols` tool and the TUI's blast-radius lookup.
type SymbolQuery struct {
	h         *Handle
	index     SymbolIndex
	projectID int64

	// wait is how long a query lets a server start before using the index.
	// Tests shorten it; production uses symbolWait.
	wait time.Duration
}

// NewSymbolQuery returns a query over h's language servers that falls back to
// index. h and index may each be nil; the other one then answers alone.
func NewSymbolQuery(h *Handle, index SymbolIndex, projectID int64) *SymbolQuery {
	return &SymbolQuery{h: h, index: index, projectID: projectID, wait: symbolWait}
}

// languageServer is one language a query considers, with its client when the
// server came up in time.
type languageServer struct {
	lang   string
	client *Client
}

// symbolResolution is the state one query gathered: the workspace root, the
// languages asked (with clients when ready) and the note explaining why a
// server could not answer.
type symbolResolution struct {
	root    string
	servers []languageServer
	note    string
}

// resolvedSymbol is where a name was found: a path relative to the root, the
// 0-based line and the UTF-16 column of the identifier on that line. hit is
// set when the answer came from the index rather than a server.
type resolvedSymbol struct {
	path  string
	line0 int
	col   int
	hit   *db.Symbol
}

// Query answers req. A lookup no server can answer is answered from the index
// with a note instead of failing.
func (q *SymbolQuery) Query(ctx context.Context, req SymbolRequest) (SymbolResult, error) {
	op := req.Op
	if op == "" {
		op = "definition"
	}
	switch op {
	case "find", "definition", "references", "hover":
	default:
		return SymbolResult{}, errors.New("symbols: op must be find, definition, references or hover")
	}
	if req.Name == "" {
		return SymbolResult{}, errors.New("symbols: name is required")
	}

	r := q.serversFor(ctx, req)
	if op == "find" {
		return q.find(ctx, req, r), nil
	}
	res, miss, ok := q.resolve(ctx, req, r)
	if !ok {
		return miss, nil
	}
	switch op {
	case "references":
		return q.references(ctx, req, r, res), nil
	case "hover":
		return q.hover(ctx, req, r, res), nil
	default:
		return q.definition(ctx, req, r, res), nil
	}
}

// References lists where the symbol at filePath:line:col is used, for the
// TUI's blast-radius lookup. ok is false when no server is ready for the
// file's language. It is a port of QueryAdapter.locations for
// textDocument/references, except that it resolves the client through
// serverLang rather than repo.DetectLanguage: the TypeScript server also
// serves JavaScript, so a .js file must reach it instead of looking for a
// "javascript" server that is never registered.
func (q *SymbolQuery) References(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	if q == nil || q.h == nil {
		return nil, false
	}
	root := q.h.Root()
	client, ok := q.h.ServerFor(serverLang(filePath))
	if !ok {
		return nil, false
	}
	raw, err := client.Request(ctx, "textDocument/references", posParams(root, filePath, line, col, map[string]any{"includeDeclaration": true}))
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
		p, err := fromFileURI(l.URI, root)
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d", p, l.Range.Start.Line+1))
	}
	return out, true
}

func (q *SymbolQuery) manager() *Manager {
	if q.h == nil {
		return nil
	}
	return q.h.Get()
}

// serversFor waits for the languages req concerns — the request path's
// language when a path is given, otherwise every configured language — and
// records the ones that are ready. note explains the first one that was not.
func (q *SymbolQuery) serversFor(ctx context.Context, req SymbolRequest) *symbolResolution {
	r := &symbolResolution{}
	if q.h != nil {
		r.root = q.h.Root()
	}
	m := q.manager()
	if m == nil {
		return r
	}
	langs := m.Languages()
	if req.Path != "" {
		if l := serverLang(req.Path); l != "" {
			langs = []string{l}
		} else {
			langs = nil
		}
	}
	for _, lang := range langs {
		ls := languageServer{lang: lang}
		if m.WaitReady(ctx, lang, q.wait) {
			if c, ok := m.ServerFor(lang); ok {
				ls.client = c
			}
		}
		if ls.client == nil && r.note == "" {
			r.note = indexNote(m, lang)
		}
		r.servers = append(r.servers, ls)
	}
	return r
}

// indexNote explains a fallback to the index for a language whose server did
// not come up.
func indexNote(m *Manager, lang string) string {
	if reason := m.Disabled(lang); reason != "" {
		return fmt.Sprintf("(%s; answered from index)", reason)
	}
	return "(language server still starting; answered from index)"
}

// ready returns the clients that can answer.
func (r *symbolResolution) ready() []*Client {
	var out []*Client
	for _, s := range r.servers {
		if s.client != nil {
			out = append(out, s.client)
		}
	}
	return out
}

// clientFor returns the client serving path's language, that language, and
// whether such a client is ready.
func (r *symbolResolution) clientFor(path string) (*Client, string, bool) {
	lang := serverLang(path)
	for _, s := range r.servers {
		if s.client != nil && s.lang == lang {
			return s.client, s.lang, true
		}
	}
	return nil, r.languageFor(path), false
}

// languageFor names the language a message about path should mention.
func (r *symbolResolution) languageFor(path string) string {
	if lang := serverLang(path); lang != "" {
		return lang
	}
	for _, s := range r.servers {
		if s.lang != "" {
			return s.lang
		}
	}
	return ""
}

// symbolRequest issues one request with the per-request timeout.
func symbolRequest(ctx context.Context, c *Client, method string, params any) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, symbolRequestTimeout)
	defer cancel()
	return c.Request(rctx, method, params)
}

// workspaceSymbol runs the server's fuzzy symbol search on every ready client
// and concatenates the results.
func (q *SymbolQuery) workspaceSymbol(ctx context.Context, r *symbolResolution, query string) []SymbolInformation {
	var out []SymbolInformation
	for _, c := range r.ready() {
		raw, err := symbolRequest(ctx, c, "workspace/symbol", map[string]any{"query": query})
		if err != nil {
			continue
		}
		var syms []SymbolInformation
		if jsonUnmarshal(raw, &syms) != nil {
			continue
		}
		out = append(out, syms...)
	}
	return out
}

// find answers op=find: a fuzzy name search, by server when one is ready and
// by the index otherwise.
func (q *SymbolQuery) find(ctx context.Context, req SymbolRequest, r *symbolResolution) SymbolResult {
	var lines []string
	if len(r.ready()) > 0 {
		for _, s := range q.workspaceSymbol(ctx, r, req.Name) {
			if len(lines) >= maxFindLines {
				break
			}
			p := relPath(r.root, s.Location.URI)
			if p == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d %s %s", p, s.Location.Range.Start.Line+1, symbolKindName(s.Kind), s.Name))
		}
	} else if q.index != nil {
		syms, err := q.index.FindSymbols(q.projectID, req.Name, "", maxFindLines)
		if err == nil {
			for _, s := range syms {
				if len(lines) >= maxFindLines {
					break
				}
				lines = append(lines, fmt.Sprintf("%s:%d %s %s", s.FilePath, s.LineStart, s.Kind, s.Name))
			}
		}
	}
	return SymbolResult{Summary: fmt.Sprintf("%d symbols", len(lines)), Content: strings.Join(lines, "\n")}
}

// resolve turns req's name into a position. ok is false when no position
// could be found, in which case the returned result is the answer to give.
func (q *SymbolQuery) resolve(ctx context.Context, req SymbolRequest, r *symbolResolution) (resolvedSymbol, SymbolResult, bool) {
	base, container := splitQualified(req.Name)

	// Path plus a line addresses one occurrence directly: the identifier is
	// on that line, so no symbol search is needed.
	if req.Path != "" && req.Line > 0 {
		text, _ := readSourceLine(r.root, req.Path, req.Line)
		col := identifierColumn(text, base)
		if col < 0 {
			return resolvedSymbol{}, SymbolResult{
				Summary: "not found",
				Content: fmt.Sprintf("no identifier %s on %s:%d", req.Name, req.Path, req.Line),
			}, false
		}
		return resolvedSymbol{path: req.Path, line0: req.Line - 1, col: col}, SymbolResult{}, true
	}

	// Name resolution through the server's symbol search, keeping exact
	// name matches only.
	var found []SymbolInformation
	if len(r.ready()) > 0 {
		found = q.workspaceSymbol(ctx, r, base)
		var matches []SymbolInformation
		for _, s := range found {
			if !symbolMatch(s, req.Name, base, container) {
				continue
			}
			if req.Path != "" && relPath(r.root, s.Location.URI) != filepath.ToSlash(req.Path) {
				continue
			}
			matches = append(matches, s)
		}
		switch {
		case len(matches) == 1:
			m := matches[0]
			path := relPath(r.root, m.Location.URI)
			line0, col := m.Location.Range.Start.Line, m.Location.Range.Start.Character
			if text, ok := readSourceLine(r.root, path, line0+1); ok {
				if c := identifierColumn(text, base); c >= 0 {
					col = c
				}
			}
			return resolvedSymbol{path: path, line0: line0, col: col}, SymbolResult{}, true
		case len(matches) > 1:
			return resolvedSymbol{}, ambiguousResult(r, matches), false
		}
	}

	// Index branch, also used when no server is ready.
	var rows []db.Symbol
	if q.index != nil {
		rows, _ = q.index.FindSymbols(q.projectID, base, "", indexLookupLimit)
	}
	var hits []db.Symbol
	for _, s := range rows {
		if s.Name != base {
			continue
		}
		if container != "" && !strings.Contains(s.Receiver, container) {
			continue
		}
		if req.Path != "" && filepath.ToSlash(s.FilePath) != filepath.ToSlash(req.Path) {
			continue
		}
		hits = append(hits, s)
	}
	if len(hits) == 1 {
		hit := hits[0]
		col := 0
		if text, ok := readSourceLine(r.root, hit.FilePath, hit.LineStart); ok {
			if c := identifierColumn(text, base); c >= 0 {
				col = c
			}
		}
		return resolvedSymbol{path: hit.FilePath, line0: hit.LineStart - 1, col: col, hit: &hit}, SymbolResult{}, true
	}
	return resolvedSymbol{}, notFoundResult(req, r, found, rows), false
}

// ambiguousResult lists the candidates when a name matches more than one
// symbol.
func ambiguousResult(r *symbolResolution, matches []SymbolInformation) SymbolResult {
	var b strings.Builder
	b.WriteString("ambiguous symbol; pass path or path+line:")
	for _, s := range matches {
		p := relPath(r.root, s.Location.URI)
		if p == "" {
			continue
		}
		fmt.Fprintf(&b, "\n  %s:%d", p, s.Location.Range.Start.Line+1)
	}
	return SymbolResult{Summary: "ambiguous", Content: b.String()}
}

// notFoundResult reports a name nothing matched, with up to maxNearMatches
// close names: the server's fuzzy hits when a server answered, the index's
// rows otherwise.
func notFoundResult(req SymbolRequest, r *symbolResolution, found []SymbolInformation, rows []db.Symbol) SymbolResult {
	content := "no symbol named " + req.Name
	var names []string
	seen := map[string]bool{}
	if len(r.ready()) > 0 {
		for _, s := range found {
			if s.Name == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	} else {
		for _, s := range rows {
			if s.Name == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	if len(names) > maxNearMatches {
		names = names[:maxNearMatches]
	}
	if len(names) > 0 {
		content += "\nclose matches: " + strings.Join(names, ", ")
	}
	return SymbolResult{Summary: "not found", Content: content}
}

// definition answers op=definition at res, from the server when it can and
// from the index hit otherwise.
func (q *SymbolQuery) definition(ctx context.Context, req SymbolRequest, r *symbolResolution, res resolvedSymbol) SymbolResult {
	if client, _, ok := r.clientFor(res.path); ok {
		raw, err := symbolRequest(ctx, client, "textDocument/definition", posParams(r.root, res.path, res.line0, res.col, nil))
		if err == nil {
			if locs := decodeLocations(raw); len(locs) > 0 {
				return SymbolResult{Summary: fmt.Sprintf("%d definition(s)", len(locs)), Content: appendNote(renderLocations(r.root, locs), r.note)}
			}
		}
	}
	if res.hit != nil {
		return SymbolResult{Summary: "1 definition(s)", Content: appendNote(renderExcerpt(r.root, res.hit.FilePath, res.hit.LineStart), r.note)}
	}
	return SymbolResult{Summary: "not found", Content: appendNote("no definition for "+req.Name, r.note)}
}

// references answers op=references at res. The index cannot answer it, so a
// missing server is reported.
func (q *SymbolQuery) references(ctx context.Context, req SymbolRequest, r *symbolResolution, res resolvedSymbol) SymbolResult {
	client, lang, ok := r.clientFor(res.path)
	if !ok {
		return SymbolResult{Summary: "no server", Content: needServer("references need a language server for", lang, r.note)}
	}
	extra := map[string]any{"context": map[string]any{"includeDeclaration": false}}
	raw, err := symbolRequest(ctx, client, "textDocument/references", posParams(r.root, res.path, res.line0, res.col, extra))
	if err != nil {
		return SymbolResult{Summary: "no server", Content: needServer("references need a language server for", lang, r.note)}
	}
	var locs []Location
	_ = jsonUnmarshal(raw, &locs)
	var lines []string
	for _, l := range locs {
		if len(lines) >= maxReferences {
			break
		}
		p := relPath(r.root, l.URI)
		if p == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s:%d", p, l.Range.Start.Line+1))
	}
	content := strings.Join(lines, "\n")
	if len(locs) > maxReferences {
		content += fmt.Sprintf("\n… %d more", len(locs)-maxReferences)
	}
	if content == "" {
		content = "no references to " + req.Name
	}
	return SymbolResult{Summary: fmt.Sprintf("%d reference(s)", len(locs)), Content: appendNote(content, r.note)}
}

// hover answers op=hover at res. The index cannot answer it, so a missing
// server is reported.
func (q *SymbolQuery) hover(ctx context.Context, req SymbolRequest, r *symbolResolution, res resolvedSymbol) SymbolResult {
	client, lang, ok := r.clientFor(res.path)
	if !ok {
		return SymbolResult{Summary: "no server", Content: needServer("hover needs a language server for", lang, r.note)}
	}
	raw, err := symbolRequest(ctx, client, "textDocument/hover", posParams(r.root, res.path, res.line0, res.col, nil))
	if err == nil {
		var h Hover
		if jsonUnmarshal(raw, &h) == nil {
			if text := flattenHoverContents(h.Contents); text != "" {
				return SymbolResult{Summary: "hover", Content: appendNote(truncateRunes(text, maxHoverChars), r.note)}
			}
		}
	}
	return SymbolResult{Summary: "no server", Content: needServer("hover needs a language server for", lang, r.note)}
}

// needServer renders "<sentence> <lang> <note>", dropping the parts it has no
// value for.
func needServer(sentence, lang, note string) string {
	msg := sentence
	if lang != "" {
		msg += " " + lang
	}
	if note != "" {
		msg += " " + note
	}
	return msg
}

// appendNote adds the index-fallback note after a blank line.
func appendNote(content, note string) string {
	if note == "" {
		return content
	}
	if content == "" {
		return note
	}
	return content + "\n\n" + note
}

// symbolMatch reports whether s is the symbol named name, where base is name's
// last dot-separated segment and container the part before it.
func symbolMatch(s SymbolInformation, name, base, container string) bool {
	if s.Name == name {
		return true
	}
	if s.Name != base {
		return false
	}
	return container == "" || strings.HasSuffix(s.ContainerName, container) || strings.Contains(s.ContainerName, container)
}

// splitQualified splits Type.Method into its parts. A name with no dot has an
// empty container.
func splitQualified(name string) (base, container string) {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:], name[:i]
	}
	return name, ""
}

// symbolKindName names an LSP SymbolKind for a find listing.
func symbolKindName(k int) string {
	switch k {
	case 5:
		return "class"
	case 6:
		return "method"
	case 8:
		return "field"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 23:
		return "struct"
	}
	return "symbol"
}

// decodeLocations accepts the definition result, an array or a single
// Location.
func decodeLocations(raw []byte) []Location {
	var locs []Location
	if jsonUnmarshal(raw, &locs) == nil {
		return locs
	}
	var one Location
	if jsonUnmarshal(raw, &one) == nil && one.URI != "" {
		return []Location{one}
	}
	return nil
}

// renderLocations renders up to maxDefinitionLocs definitions as "rel:line"
// plus a short excerpt.
func renderLocations(root string, locs []Location) string {
	var blocks []string
	for _, l := range locs {
		if len(blocks) >= maxDefinitionLocs {
			break
		}
		if p := relPath(root, l.URI); p != "" {
			blocks = append(blocks, renderExcerpt(root, p, l.Range.Start.Line+1))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// renderExcerpt renders "path:line" followed by up to maxDefinitionLines
// source lines starting at that 1-based line.
func renderExcerpt(root, path string, line int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d", path, line)
	for _, l := range sourceLines(root, path, line, maxDefinitionLines) {
		b.WriteString("\n")
		b.WriteString(l)
	}
	return b.String()
}

// relPath returns uri as a path relative to root, or "" when it does not
// denote a file inside root.
func relPath(root, uri string) string {
	abs, err := fromFileURI(uri, root)
	if err != nil {
		return ""
	}
	if root == "" {
		return abs
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// readSourceLine returns the text of the 1-based line of path, which is
// resolved against root when relative.
func readSourceLine(root, path string, line int) (string, bool) {
	lines := sourceLines(root, path, line, 1)
	if len(lines) == 0 {
		return "", false
	}
	return lines[0], true
}

// sourceLines returns up to max lines of path starting at the 1-based line,
// with trailing blank lines dropped.
func sourceLines(root, path string, line, max int) []string {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	start := line - 1
	if start < 0 || start >= len(lines) {
		return nil
	}
	end := start + max
	if end > len(lines) {
		end = len(lines)
	}
	out := append([]string(nil), lines[start:end]...)
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	out := make([]rune, 0, n)
	for _, r := range s {
		if len(out) >= n {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

// identifierColumn returns the UTF-16 column at which name starts on the
// line, or -1 when the line does not contain it as a whole identifier.
// Identifier runes are letters, digits and "_"; LSP positions count UTF-16
// code units, so a multi-byte character before the identifier shifts the
// column past its byte offset.
func identifierColumn(lineText, name string) int {
	if name == "" {
		return -1
	}
	for from := 0; from <= len(lineText); {
		i := strings.Index(lineText[from:], name)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(name)
		if !identifierRuneBefore(lineText, i) && !identifierRuneAt(lineText, end) {
			n := 0
			for _, r := range lineText[:i] {
				n += utf16.RuneLen(r)
			}
			return n
		}
		from = i + 1
	}
	return -1
}

func identifierRuneBefore(s string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isIdentifierRune(r)
}

func identifierRuneAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isIdentifierRune(r)
}

func isIdentifierRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
