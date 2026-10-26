package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// subjectFirstTool reports whether a tool's row should lead with its subject
// rather than its name; viewmodel.SubjectFirstTool decides it.
func subjectFirstTool(name string) bool { return viewmodel.SubjectFirstTool(name) }

// shellSubject renders a command row: the command itself, then its exit
// code and how long it took.
func shellSubject(event registry.AuditEvent) string { return shellSubjectIn(event, "") }

// shellSubjectIn is shellSubject with the session's active root, so a command
// that begins by entering the working tree reads "⌂ pytest" rather than
// repeating a path the row's context already implies.
func shellSubjectIn(event registry.AuditEvent, workdir string) string {
	cmd := toolTarget(event)
	if cmd == "" {
		return ""
	}
	parts := []string{viewmodel.ElideWorkdir(cmd, workdir)}
	if event.CommandExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *event.CommandExitCode))
	}
	if event.Duration > 0 {
		parts = append(parts, formatElapsed(event.Duration))
	}
	return strings.Join(parts, dimSeparator)
}

// searchQualifier returns the short label for a search-family tool;
// viewmodel.SearchQualifier decides it.
func searchQualifier(name string) (string, bool) { return viewmodel.SearchQualifier(name) }

// searchSubject renders `search "gutterPrefix"` — the qualifier plus the
// query. The outcome stays in ResultSummary, which these tools already
// phrase well ("4 matches", "no matches", "3 found"), and the caller
// appends it.
//
// json.query is special — and retired; the entry survives for persisted
// transcripts: its primary subject was the jq expression, not the file path.
// For every live search-family tool toolTarget's path-first ordering is
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

// searchTarget returns the subject string for a search-family row;
// viewmodel.SearchTarget decides it.
func searchTarget(event registry.AuditEvent) string { return viewmodel.SearchTarget(event) }

// argString extracts a string field from a tool-call's JSON args;
// viewmodel.ArgString decides it.
func argString(args json.RawMessage, key string) string { return viewmodel.ArgString(args, key) }

// symbolSubject renders "path › A(), B() +2" for an event carrying symbol
// attribution, grouped by file in first-seen order. It returns "" when the
// event carries no symbols, which is the common case on languages without
// a tree-sitter grammar.
func symbolSubject(event registry.AuditEvent) string { return viewmodel.SymbolSubject(event) }

// symbolLabel renders one symbol: callables get "()" so a function reads
// differently from a type at a glance.
func symbolLabel(s registry.SymbolRef) string { return viewmodel.SymbolLabel(s) }

// diffStat summarises a unified diff as "+N −M"; viewmodel.DiffStat decides
// it.
func diffStat(diff string) string { return viewmodel.DiffStat(diff) }
