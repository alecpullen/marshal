package viewmodel

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// The functions in this file turn tree payloads into the short phrases every
// client shows: a step's headline, a tool call's subject, an owner's name.
// They are plain text with no styling, so the TUI and the web wire projection
// say the same thing about the same node.

// maxSubjectSymbols is how many symbol names a row names before collapsing
// the rest into a "+N" count. Two is enough to see the shape of a change
// without the row becoming a list.
const maxSubjectSymbols = 2

// StepOwner is the actor a step ran as: its label, else its role with the
// sdd_ prefix dropped. Empty for the orchestrator.
func StepOwner(st session.Step) string {
	if st.Actor.Role == "" && st.Actor.Label == "" {
		return ""
	}
	if st.Actor.Label != "" {
		return st.Actor.Label
	}
	return strings.ReplaceAll(strings.TrimPrefix(st.Actor.Role, "sdd_"), "_", " ")
}

// StepRoleWord is the owner shortened to its role word ("reviewer #1" →
// "reviewer"), the form that survives a narrow header.
func StepRoleWord(st session.Step) string {
	o := StepOwner(st)
	if i := strings.IndexAny(o, " #"); i > 0 {
		o = o[:i]
	}
	if st.Actor.Role != "" && strings.Contains(st.Actor.Role, "branch") {
		return "branch reviewer"
	}
	return o
}

// StepHeadline picks the header text. A narrated step uses the first sentence
// of its first narration; the remainder (and any later narration) becomes the
// continuation. Without narration the headline is inferred from the tool rows
// and rendered as such.
func StepHeadline(si *StepInfo, rows []*Node) (head, rest string, inferred bool) {
	var texts []string
	for _, m := range si.Narration {
		if t := strings.TrimSpace(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) > 0 {
		h, r := FirstSentence(texts[0])
		parts := []string{}
		if r != "" {
			parts = append(parts, r)
		}
		parts = append(parts, texts[1:]...)
		return StripEmphasis(h), strings.Join(parts, "\n\n"), false
	}
	if h := InferHeadline(rows); h != "" {
		return h, "", true
	}
	if len(si.Thinking) > 0 || si.LiveThinking != "" {
		return "thinking", "", true
	}
	return "working", "", true
}

// FirstSentence splits s at the first ". ", "! ", "? " or newline that comes
// after at least 8 runes. A sentence-final mark at the very end of s stays
// with the head.
func FirstSentence(s string) (head, rest string) {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\n':
			if i >= 8 {
				return strings.TrimSpace(string(runes[:i])), strings.TrimSpace(string(runes[i+1:]))
			}
		case (r == '.' || r == '!' || r == '?') && i >= 7:
			if i+1 < len(runes) && unicode.IsSpace(runes[i+1]) {
				return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
			}
		}
	}
	return s, ""
}

// StripEmphasis removes markdown emphasis and code markers from a headline,
// which is rendered as plain text.
func StripEmphasis(s string) string {
	return strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "~~", "").Replace(s)
}

// InferHeadline describes what a step did from its tool rows when the model
// did not narrate: up to two clauses joined by " · ", then "…" if more
// follow. Clients mark it as inferred because it is a guess at intent, not
// the agent's own words.
func InferHeadline(rows []*Node) string {
	type clause struct {
		kind  string
		count int
		text  string
	}
	var order []string
	byKind := map[string]*clause{}
	touch := func(kind string) *clause {
		c, ok := byKind[kind]
		if !ok {
			c = &clause{kind: kind}
			byKind[kind] = c
			order = append(order, kind)
		}
		return c
	}
	visit := func(name, target string, n int) {
		switch {
		case name == "file.read":
			touch("read").count += n
		case IsSearchTool(name):
			c := touch("search")
			c.count += n
			if c.text == "" && target != "" {
				c.text = target
			}
		case name == "shell.run" || name == "test.run":
			c := touch("ran:" + name)
			c.count += n
			if c.text == "" {
				if f := strings.Fields(target); len(f) > 0 {
					c.text = f[0]
				}
			}
		case name == "file.write_patch" || name == "patch.apply" || strings.HasPrefix(name, "file.write"):
			c := touch("edit")
			c.count += n
			if c.text == "" && target != "" {
				c.text = filepath.Base(target)
			}
		case name == "agent.run":
			touch("agents").count += n
		default:
			c := touch("other:" + name)
			c.count += n
			c.text = DisplayToolName(name)
		}
	}
	for _, r := range rows {
		switch {
		case r.Kind == KindSubagent:
			visit("agent.run", "", 1)
		case r.Active != nil:
			visit(r.Active.Name, strings.TrimPrefix(r.Active.Args, "$ "), 1)
		default:
			for _, ev := range r.Tools {
				visit(ev.ToolName, ToolTarget(ev), 1)
			}
		}
	}
	var parts []string
	for _, k := range order {
		c := byKind[k]
		switch {
		case k == "read":
			parts = append(parts, fmt.Sprintf("read %d %s", c.count, plural(c.count, "file", "files")))
		case k == "search":
			if c.text != "" {
				parts = append(parts, fmt.Sprintf("searched %q", c.text))
			} else {
				parts = append(parts, "searched")
			}
		case strings.HasPrefix(k, "ran:"):
			if c.text != "" {
				parts = append(parts, "ran "+c.text+" …")
			} else {
				parts = append(parts, "ran a command")
			}
		case k == "edit":
			if c.text != "" {
				parts = append(parts, "edited "+c.text)
			} else {
				parts = append(parts, fmt.Sprintf("edited %d %s", c.count, plural(c.count, "file", "files")))
			}
		case k == "agents":
			parts = append(parts, fmt.Sprintf("dispatched %d %s", c.count, plural(c.count, "agent", "agents")))
		default:
			parts = append(parts, c.text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts[:min(len(parts), 2)], " · ")
	if len(parts) > 2 {
		out += " …"
	}
	return out
}

// IsSearchTool reports whether a tool's subject is a query.
func IsSearchTool(name string) bool {
	return strings.HasPrefix(name, "repo.search") || strings.HasPrefix(name, "codebase.search") ||
		strings.HasPrefix(name, "symbols.") || name == "web.search" || strings.HasPrefix(name, "search.")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ToolTarget returns the short human-facing subject of a tool call — the
// path it read, the command it ran, the query it searched for.
func ToolTarget(event registry.AuditEvent) string {
	if s := SymbolSubject(event); s != "" {
		return s
	}
	if len(event.FilesChanged) > 0 {
		return event.FilesChanged[0]
	}
	if len(event.Args) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(event.Args, &args); err != nil {
		return ""
	}
	for _, key := range []string{"path", "command", "query", "name"} {
		if s, ok := args[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// SymbolSubject renders "path › A(), B() +2" for an event carrying symbol
// attribution, grouped by file in first-seen order. It returns "" when the
// event carries no symbols, which is the common case on languages without
// a tree-sitter grammar.
func SymbolSubject(event registry.AuditEvent) string {
	if len(event.Symbols) == 0 {
		return ""
	}
	byFile := map[string][]string{}
	var order []string
	for _, s := range event.Symbols {
		if _, seen := byFile[s.File]; !seen {
			order = append(order, s.File)
		}
		byFile[s.File] = append(byFile[s.File], SymbolLabel(s))
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
	return strings.Join(parts, " · ")
}

// SymbolLabel renders one symbol: callables get "()" so a function reads
// differently from a type at a glance.
func SymbolLabel(s registry.SymbolRef) string {
	if s.Kind == "function" || s.Kind == "method" {
		return s.Name + "()"
	}
	return s.Name
}

var toolDisplayNames = map[string]string{
	"file.read":        "Read file",
	"file.write_patch": "Edit file",
	"patch.apply":      "Apply patch",
	"shell.run":        "Run command",
	"test.run":         "Run tests",
	"repo.search":      "Search repo",
	"codebase.search":  "Codebase search",
	"json.query":       "Query JSON",
	"csv.inspect":      "Inspect CSV",
	"web.fetch":        "Fetch page",
	"web.search":       "Search web",
	"git.status":       "Git status",
	"git.diff":         "Git diff",
	"symbols.find":     "Find symbols",
	"todos":            "Update todos",
	"mode.request":     "Switch mode",
	"question.ask":     "Ask question",
	"ask_user":         "Ask user",
	"agent.run":        "Run subagent",
	// agent.await is multipurpose (subagents, background jobs, watches), so
	// the transcript row describes the action, not the target class.
	"agent.await": "Waiting…",
}

// DisplayToolName returns a human-readable label for a tool. Unknown tools
// get a title-cased, dot-to-space transformation (only the first segment
// is capitalized).
func DisplayToolName(name string) string {
	if label, ok := toolDisplayNames[name]; ok {
		return label
	}
	parts := strings.Split(name, ".")
	if len(parts) > 0 && len(parts[0]) > 0 {
		parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	}
	return strings.Join(parts, " ")
}
