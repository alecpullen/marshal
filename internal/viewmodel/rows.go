package viewmodel

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// callVerbs maps a tool-name prefix to the verb its row leads with. The first
// matching prefix wins, so write tools come before the broader file prefix.
var callVerbs = []struct{ prefix, verb string }{
	{"file.read", "read"},
	{"file.write_patch", "edit"},
	{"file.edit", "edit"},
	{"patch.", "edit"},
	{"file.write", "write"},
	{"shell.", "shell"},
	{"test.", "shell"},
	{"repo.search", "search"},
	{"codebase.search", "search"},
	{"codebase_search", "search"},
	{"search.", "search"},
	{"symbols", "search"},
	{"lsp.", "read"},
	{"agent.", "agent"},
	{"web.", "web"},
	{"browser.", "web"},
	{"todo.write", "plan"},
	{"mcp.", "mcp"},
}

// CallVerb is the verb a call's row leads with. A tool outside the table
// uses the first segment of its name ("git.status" reads "git").
func CallVerb(name string) string {
	for _, v := range callVerbs {
		if strings.HasPrefix(name, v.prefix) {
			return v.verb
		}
	}
	first, _, _ := strings.Cut(name, ".")
	return first
}

// FallbackSubject is a row's subject when the call's arguments name none:
// the rest of the tool's name after its first segment ("git.status" reads
// "git status", "agent.await" reads "agent await"), unless that only repeats
// the verb ("file.read" reads "read", not "read read").
func FallbackSubject(name string) string {
	_, rest, ok := strings.Cut(name, ".")
	if !ok || rest == CallVerb(name) {
		return ""
	}
	return strings.ReplaceAll(rest, ".", " ")
}

// CallSubject is what a settled call acted on: the command it ran, the file
// or symbols it touched, the query it searched for, or the task it handed to
// a subagent. It is model-authored text, so it is scrubbed here.
func CallSubject(ev registry.AuditEvent, workdir string) string {
	var s string
	switch {
	case IsShellFamily(ev.ToolName):
		s = strutil.ElideWorkdir(ToolTarget(ev), workdir)
	case ev.ToolName == "agent.run":
		s = ArgString(ev.Args, "description")
	default:
		if sym := SymbolSubject(ev); sym != "" && SubjectFirstTool(ev.ToolName) {
			s = sym
		} else if q, ok := SearchQualifier(ev.ToolName); ok {
			if t := SearchTarget(ev); t != "" {
				s = strconv.Quote(t)
				// The verb already says "search"; any other qualifier tells
				// two kinds of search apart.
				if q != "search" {
					s = q + " " + s
				}
			}
		} else {
			s = ToolTarget(ev)
		}
	}
	if s == "" {
		s = FallbackSubject(ev.ToolName)
	}
	return strutil.ScrubText(firstLine(s))
}

// SubjectFirstTool reports whether a tool's row should lead with its subject
// rather than its name.
//
// It is deliberately a small allow-list. For a file or symbol tool the verb
// is the least interesting thing on the line and the subject is the point.
// For shell, git, and agent calls the verb IS the content, so those keep
// the tool-name-first shape.
func SubjectFirstTool(name string) bool {
	switch name {
	case "file.write_patch", "file.edit", "file.write", "file.read", "symbols", "symbols.find":
		return true
	}
	return IsShellFamily(name)
}

// CallersSymbol is the symbol a callers lookup asks about for ev: the first
// resolved symbol of a call that changed it, in a subject-first tool other
// than file.read. ok is false when there is none. An unresolved position is
// never asked about: a lookup at a guessed column returns confidently wrong
// callers, which is worse than none.
func CallersSymbol(ev registry.AuditEvent) (ref registry.SymbolRef, ok bool) {
	if !SubjectFirstTool(ev.ToolName) || ev.ToolName == "file.read" {
		return registry.SymbolRef{}, false
	}
	for _, s := range ev.Symbols {
		if s.Resolved {
			return s, true
		}
	}
	return registry.SymbolRef{}, false
}

// searchQualifiers gives each search tool a short, distinguishable label: a
// literal grep and a semantic search answer different questions and cost very
// different amounts, and a reader must be able to tell them apart at a glance.
//
// json.query and csv.inspect are retired (context-diet Tier 2a); their
// entries stay so persisted transcripts still render a qualifier instead of
// none.
var searchQualifiers = map[string]string{
	"repo.search":     "search",
	"codebase.search": "semantic",
	"symbols":         "symbols",
	"symbols.find":    "symbols",
	"json.query":      "json",
	"csv.inspect":     "csv",
}

// SearchQualifier returns the short label for a search-family tool.
func SearchQualifier(name string) (string, bool) {
	q, ok := searchQualifiers[name]
	return q, ok
}

// SearchTarget returns the subject string for a search-family row. It
// prefers the jq expression for json.query (where the expression is the
// point and the path is context) and the symbol name for symbols (where the
// name is the point and a pinning path is context), and falls back to
// ToolTarget's path-first ordering for every other search tool.
func SearchTarget(ev registry.AuditEvent) string {
	if ev.ToolName == "json.query" {
		if s := ArgString(ev.Args, "query"); s != "" {
			return s
		}
	}
	if ev.ToolName == "symbols" {
		if s := ArgString(ev.Args, "name"); s != "" {
			return s
		}
	}
	return ToolTarget(ev)
}

// ArgString extracts a string field from a tool-call's JSON args. Returns
// "" when the args are missing, malformed, or the field is absent/empty.
func ArgString(args json.RawMessage, key string) string {
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

// HookHint picks the single highest-signal hook event from the metadata slice
// for the tool summary line. Priority (most specific first): block > rewrote >
// failed-open > generic count. Returns "" when there are no hooks to
// summarize.
func HookHint(hooks []registry.HookMetadata) string {
	if len(hooks) == 0 {
		return ""
	}
	for _, h := range hooks {
		if h.Decision == "block" || h.Decision == "halt" {
			return "hook blocked"
		}
	}
	for _, h := range hooks {
		if h.Rewrote {
			return "hook rewrote"
		}
	}
	for _, h := range hooks {
		if h.FailedOpen {
			return "hook failed-open"
		}
	}
	return fmt.Sprintf("hooks %d", len(hooks))
}

// DiffStat summarises a unified diff as "+N −M". Returns "" for a diff with
// no changed lines, so a row never carries an empty "+0 −0".
//
// This is formatting of data the row already holds, not derivation: the
// diff is on the audit event, and splitDiffFiles in transcript.go already
// parses diffs in this layer.
func DiffStat(diff string) string {
	added, removed := diffCounts(diff)
	if added == 0 && removed == 0 {
		return ""
	}
	return fmt.Sprintf("+%d −%d", added, removed)
}

// diffCounts is the number of added and removed lines in a unified diff.
func diffCounts(diff string) (added, removed int) {
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
	return added, removed
}

// RunningSubject is what a running call acts on: its path, else the first
// line of its arguments, else the rest of its tool name. Scrubbed.
func RunningSubject(atc session.ActiveToolCall) string {
	subject := atc.Path
	if subject == "" {
		subject = firstLine(atc.Args)
	}
	if subject == "" {
		subject = FallbackSubject(atc.Name)
	}
	return strutil.ScrubText(subject)
}

// SummaryLine is the first line of the call's display summary, scrubbed: what
// its row shows.
func SummaryLine(ev registry.AuditEvent) string {
	return strutil.ScrubText(firstLine(DisplaySummary(ev)))
}

// CallDiffStat is the diff stat a call's row shows: DiffStat of the result
// for an edit or a whole-file write, and "" for any other call.
func CallDiffStat(ev registry.AuditEvent) string {
	if !isDiffTool(ev.ToolName) && ev.ToolName != "file.write" {
		return ""
	}
	return DiffStat(ev.ResultContent)
}

// FailedTailLines is how many output lines a failed row shows collapsed.
const FailedTailLines = 3

// FailedTail is the last FailedTailLines lines of a call's output, or of its
// error when it printed nothing, scrubbed with ScrubBlock and right-trimmed
// of spaces, tabs and carriage returns. It is nil when there is neither.
func FailedTail(ev registry.AuditEvent) []string {
	text := strings.TrimRight(ev.ResultContent, "\n")
	if text == "" {
		text = ev.Error
	}
	if text == "" {
		return nil
	}
	lines := strings.Split(strutil.ScrubBlock(text), "\n")
	if len(lines) > FailedTailLines {
		lines = lines[len(lines)-FailedTailLines:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	return out
}

// firstLine returns the first line of s, right-trimmed. Multi-line error
// text would break the one-row banner shape and the compact transcript
// row.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t\r")
}
