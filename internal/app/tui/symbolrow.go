package tui

import (
	"fmt"
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
