package viewmodel

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// ElideWorkdir shortens a command that starts by changing into the session's
// working tree: "cd <root> && pytest" reads as "⌂ pytest".
//
// A shell command that has to run in the worktree has to say so, and the
// path is both long and already implied by the row's context. Only a leading
// cd into the root, followed by a separator, is elided: a cd somewhere else
// is information the reader needs.
func ElideWorkdir(cmd, root string) string {
	if root == "" {
		return cmd
	}
	for _, sep := range []string{" && ", "; "} {
		for _, q := range []string{root, `"` + root + `"`, "'" + root + "'"} {
			if p := "cd " + q + sep; strings.HasPrefix(cmd, p) {
				return "⌂ " + strings.TrimPrefix(cmd, p)
			}
		}
	}
	return cmd
}

// DisplaySummary is the summary a row shows. An await's result names what
// finished, which the completion marker already says; its row just reports
// the wait.
func DisplaySummary(ev registry.AuditEvent) string {
	if ev.ToolName == "agent.await" && ev.Error == "" {
		return "waited " + compactWait(ev.Duration)
	}
	return ev.ResultSummary
}

func compactWait(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

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

// StepHeadline picks a beat's header: the first sentence of its first
// narration, with the remainder (and any later narration) as the
// continuation. A beat without narration is headless (head == ""): its rows
// render on their own, with no invented title.
func StepHeadline(si *StepInfo) (head, rest string) {
	var texts []string
	for _, m := range si.Narration {
		// A caller-built tree can hold a nil narration entry; a header must
		// never be the thing that panics a renderer.
		if m == nil {
			continue
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return "", ""
	}
	h, r := FirstSentence(texts[0])
	parts := []string{}
	if r != "" {
		parts = append(parts, r)
	}
	parts = append(parts, texts[1:]...)
	return StripEmphasis(h), strings.Join(parts, "\n\n")
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
