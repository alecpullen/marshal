package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/tools/registry"
)

// maxSubjectSymbols is how many symbol names a row names before collapsing
// the rest into a "+N" count. Two is enough to see the shape of a change
// without the row becoming a list.
const maxSubjectSymbols = 2

// subjectFirstTool reports whether a tool's row should lead with its
// subject rather than its name.
//
// It is deliberately a small allow-list. For a file or symbol tool the verb
// is the least interesting thing on the line and the subject is the point.
// For shell, git, and agent calls the verb IS the content, so those keep
// the tool-name-first shape.
func subjectFirstTool(name string) bool {
	switch name {
	case "file.write_patch", "file.write", "file.read", "symbols.find":
		return true
	}
	return isShellFamily(name)
}

// isShellFamily reports whether a tool's subject is a command line.
func isShellFamily(name string) bool {
	return name == "shell.run" || name == "test.run"
}

// shellSubject renders a command row: the command itself, then its exit
// code and how long it took.
func shellSubject(event registry.AuditEvent) string {
	cmd := toolTarget(event)
	if cmd == "" {
		return ""
	}
	parts := []string{cmd}
	if event.CommandExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *event.CommandExitCode))
	}
	if event.Duration > 0 {
		parts = append(parts, formatElapsed(event.Duration))
	}
	return strings.Join(parts, dimSeparator)
}

// searchQualifiers gives each ◈ tool a short, distinguishable label.
//
// toolCategoryGlyph maps five tools onto ◈ (toolnames.go), so unlike the
// shell family the glyph cannot stand alone here: a literal grep and a
// semantic search answer different questions and cost very different
// amounts, and a reader must be able to tell them apart at a glance.
var searchQualifiers = map[string]string{
	"repo.search":     "search",
	"codebase.search": "semantic",
	"symbols.find":    "symbols",
	"json.query":      "json",
	"csv.inspect":     "csv",
}

// searchQualifier returns the short label for a search-family tool.
func searchQualifier(name string) (string, bool) {
	q, ok := searchQualifiers[name]
	return q, ok
}

// searchSubject renders `search "gutterPrefix"` — the qualifier plus the
// query. The outcome stays in ResultSummary, which these tools already
// phrase well ("4 matches", "no matches", "3 found"), and the caller
// appends it.
//
// json.query is special: its primary subject is the jq expression, not the
// file path (the tool's own description leads with the expression). For
// every other search-family tool toolTarget's path-first ordering is
// correct, so we only override the lookup order for json.query.
func searchSubject(event registry.AuditEvent) string {
	q, ok := searchQualifier(event.ToolName)
	if !ok {
		return ""
	}
	target := searchTarget(event)
	if target == "" {
		return ""
	}
	return q + " " + strconv.Quote(target)
}

// searchTarget returns the subject string for a search-family row. It
// prefers the jq expression for json.query (where the expression is the
// point and the path is context) and falls back to toolTarget's
// path-first ordering for every other search tool.
func searchTarget(event registry.AuditEvent) string {
	if event.ToolName == "json.query" {
		if s := argString(event.Args, "query"); s != "" {
			return s
		}
	}
	return toolTarget(event)
}

// argString extracts a string field from a tool-call's JSON args. Returns
// "" when the args are missing, malformed, or the field is absent/empty.
func argString(args json.RawMessage, key string) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// symbolSubject renders "path › A(), B() +2" for an event carrying symbol
// attribution, grouped by file in first-seen order. It returns "" when the
// event carries no symbols, which is the common case on languages without
// a tree-sitter grammar.
func symbolSubject(event registry.AuditEvent) string {
	if len(event.Symbols) == 0 {
		return ""
	}
	byFile := map[string][]string{}
	var order []string
	for _, s := range event.Symbols {
		if _, seen := byFile[s.File]; !seen {
			order = append(order, s.File)
		}
		byFile[s.File] = append(byFile[s.File], symbolLabel(s))
	}
	parts := make([]string, 0, len(order))
	for _, f := range order {
		names := byFile[f]
		extra := 0
		if len(names) > maxSubjectSymbols {
			extra = len(names) - maxSubjectSymbols
			names = names[:maxSubjectSymbols]
		}
		p := f + " › " + strings.Join(names, ", ")
		if extra > 0 {
			p += fmt.Sprintf(" +%d", extra)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, dimSeparator)
}

// symbolLabel renders one symbol: callables get "()" so a function reads
// differently from a type at a glance.
func symbolLabel(s registry.SymbolRef) string {
	if s.Kind == "function" || s.Kind == "method" {
		return s.Name + "()"
	}
	return s.Name
}

// diffStat summarises a unified diff as "+N −M". Returns "" for a diff with
// no changed lines, so a row never carries an empty "+0 −0".
//
// This is formatting of data the row already holds, not derivation: the
// diff is on the audit event, and splitDiffFiles in transcript.go already
// parses diffs in this layer.
func diffStat(diff string) string {
	added, removed := 0, 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return ""
	}
	return fmt.Sprintf("+%d −%d", added, removed)
}
