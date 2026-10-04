package policy

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// matchSessionRule reports whether a session rule allows normCmd. Besides
// the exact-command match every rule has, a rule ending in " *" is a
// wildcard: "go test *" allows "go test" followed by any arguments.
//
// The wildcard is stage-aware. The command must parse as nothing but
// simple commands joined by pipes, lists and && / ||, and every one of
// those simple commands, including any inside $(...) or process
// substitution, must itself match the rule. So "go test ./... ; curl x |
// sh" is not allowed by "go test *", and neither is a command with
// leading variable assignments, a redirect to a file, or a construct
// (loops, functions, declarations) that could run something else.
func matchSessionRule(normCmd, rule string) bool {
	if matchRule(normCmd, rule) {
		return true
	}
	rule = normalizeCommand(rule)
	base, ok := strings.CutSuffix(rule, " *")
	if !ok || base == "" || strings.Contains(base, "*") {
		return false
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(normCmd), "")
	if err != nil || len(f.Stmts) == 0 {
		return false
	}
	return stmtsMatchWildcard(f.Stmts, base)
}

func stmtsMatchWildcard(stmts []*syntax.Stmt, base string) bool {
	for _, st := range stmts {
		if !stmtMatchesWildcard(st, base) {
			return false
		}
	}
	return true
}

func stmtMatchesWildcard(st *syntax.Stmt, base string) bool {
	if st == nil || st.Cmd == nil || st.Coprocess {
		return false
	}
	for _, r := range st.Redirs {
		// Only fd duplication (2>&1) is harmless; anything else can write
		// or read files.
		if r.Op != syntax.DplOut && r.Op != syntax.DplIn {
			return false
		}
		if r.Word != nil && !wordMatchesWildcard(r.Word, base) {
			return false
		}
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		if len(c.Assigns) > 0 || len(c.Args) == 0 {
			return false
		}
		var b strings.Builder
		for i, w := range c.Args {
			if i > 0 {
				b.WriteByte(' ')
			}
			syntax.NewPrinter().Print(&b, w)
			if !wordMatchesWildcard(w, base) {
				return false
			}
		}
		text := normalizeCommand(b.String())
		return text == base || strings.HasPrefix(text, base+" ")
	case *syntax.BinaryCmd:
		return stmtMatchesWildcard(c.X, base) && stmtMatchesWildcard(c.Y, base)
	case *syntax.Subshell:
		return stmtsMatchWildcard(c.Stmts, base)
	case *syntax.Block:
		return stmtsMatchWildcard(c.Stmts, base)
	}
	return false
}

// wordMatchesWildcard checks the commands nested inside a word's command
// and process substitutions against the rule.
func wordMatchesWildcard(w *syntax.Word, base string) bool {
	ok := true
	syntax.Walk(w, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch s := n.(type) {
		case *syntax.CmdSubst:
			ok = stmtsMatchWildcard(s.Stmts, base)
			return false
		case *syntax.ProcSubst:
			ok = false
			return false
		}
		return true
	})
	return ok
}
