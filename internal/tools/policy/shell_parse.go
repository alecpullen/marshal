package policy

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// maxShellDepth bounds nested-executable recursion. Exhaustion records an
// unknown effect rather than silently dropping the payload.
const maxShellDepth = 8

// AnalyzeShell parses one command into executable operations. Quoted data is
// never an operation; executable substitutions, wrappers, xargs and find
// payloads are.
func AnalyzeShell(in ShellInput) (ShellAnalysis, error) {
	if err := checkCommandControls(in.Command); err != nil {
		return ShellAnalysis{}, err
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(in.Command), "")
	if err != nil {
		return ShellAnalysis{}, fmt.Errorf("parse shell command: %w", err)
	}
	a := &shellAnalyzer{in: in}
	a.cwd = in.Dir
	if a.cwd == "" {
		a.cwd = in.WorkspaceRoot
	}
	a.walkFile(f)
	a.resolveShellScopes()
	classifyShellOperations(a.ops)
	return ShellAnalysis{Input: in, Operations: a.ops}, nil
}

type shellAnalyzer struct {
	in     ShellInput
	cwd    string
	ops    []ShellOperation
	nextID int
}

func (a *shellAnalyzer) walkFile(f *syntax.File) {
	for _, stmt := range f.Stmts {
		a.walkStmt(stmt, 0)
	}
}

func (a *shellAnalyzer) walkStmt(stmt *syntax.Stmt, depth int) {
	if stmt == nil {
		return
	}
	if depth > maxShellDepth {
		a.addUnknown(depth)
		return
	}
	a.walkRedirects(stmt.Redirs, depth)
	before := len(a.ops)
	a.walkCommand(stmt.Cmd, depth)
	if hasWriteRedirect(stmt.Redirs) && len(a.ops) > before {
		a.ops[len(a.ops)-1].Effects |= ShellWrite
	}
}

// hasWriteRedirect reports whether a statement redirects output to a file.
func hasWriteRedirect(redirs []*syntax.Redirect) bool {
	for _, r := range redirs {
		if r == nil {
			continue
		}
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll:
			return true
		}
	}
	return false
}

func (a *shellAnalyzer) walkRedirects(redirs []*syntax.Redirect, depth int) {
	for _, r := range redirs {
		if r == nil {
			continue
		}
		if r.Hdoc != nil {
			a.walkWordExecutables(r.Hdoc, depth)
		}
		if r.Word != nil {
			a.walkWordExecutables(r.Word, depth)
		}
	}
}

func (a *shellAnalyzer) walkCommand(cmd syntax.Command, depth int) {
	switch c := cmd.(type) {
	case *syntax.CallExpr:
		a.addCall(c, depth)
	case *syntax.BinaryCmd:
		saved := a.cwd
		a.walkStmt(c.X, depth)
		afterX := a.cwd
		if c.Op == syntax.OrStmt {
			// The right side runs only when the left failed, so it starts
			// from the cwd the left started from.
			a.cwd = saved
		}
		a.walkStmt(c.Y, depth)
		if c.Op == syntax.OrStmt {
			// After the alternative, the cwd is the left's success path or
			// the right's path; keep the right's as the conservative choice.
			_ = afterX
		}
	case *syntax.Block:
		for _, s := range c.Stmts {
			a.walkStmt(s, depth)
		}
	case *syntax.Subshell:
		saved := a.cwd
		for _, s := range c.Stmts {
			a.walkStmt(s, depth)
		}
		a.cwd = saved
	case *syntax.IfClause:
		for _, s := range c.Then {
			a.walkStmt(s, depth)
		}
		if c.Else != nil {
			a.walkCommand(c.Else, depth)
		}
	case *syntax.ForClause:
		for _, s := range c.Do {
			a.walkStmt(s, depth)
		}
	case *syntax.WhileClause:
		for _, s := range c.Do {
			a.walkStmt(s, depth)
		}
	case *syntax.CaseClause:
		for _, item := range c.Items {
			for _, s := range item.Stmts {
				a.walkStmt(s, depth)
			}
		}
	case *syntax.FuncDecl:
		a.walkStmt(c.Body, depth)
	case *syntax.TimeClause:
		a.walkStmt(c.Stmt, depth)
	case *syntax.CoprocClause:
		a.walkStmt(c.Stmt, depth)
	}
}

// walkWordExecutables finds executable substitutions inside a word. Single
// quotes are data; double quotes may contain executable substitutions.
func (a *shellAnalyzer) walkWordExecutables(w *syntax.Word, depth int) {
	if w == nil {
		return
	}
	for _, part := range w.Parts {
		a.walkPartExecutables(part, depth)
	}
}

func (a *shellAnalyzer) walkPartExecutables(part syntax.WordPart, depth int) {
	switch p := part.(type) {
	case *syntax.SglQuoted:
		// Single-quoted data never recurses.
		return
	case *syntax.DblQuoted:
		for _, dp := range p.Parts {
			a.walkPartExecutables(dp, depth)
		}
	case *syntax.CmdSubst:
		saved := a.cwd
		for _, s := range p.Stmts {
			a.walkStmt(s, depth+1)
		}
		a.cwd = saved
	case *syntax.ProcSubst:
		saved := a.cwd
		for _, s := range p.Stmts {
			a.walkStmt(s, depth+1)
		}
		a.cwd = saved
	}
}

func (a *shellAnalyzer) addUnknown(depth int) {
	a.ops = append(a.ops, ShellOperation{
		ID:      a.nextID,
		Effects: ShellUnknown,
		Reasons: []PolicyReason{{Code: ReasonDepthLimit, OperationID: a.nextID, Message: "nested shell depth limit reached"}},
	})
	a.nextID++
}

func (a *shellAnalyzer) addCall(call *syntax.CallExpr, depth int) {
	if call == nil || len(call.Args) == 0 {
		return
	}
	argv := make([]ShellWord, 0, len(call.Args))
	for _, w := range call.Args {
		argv = append(argv, a.decodeWord(w))
	}
	// Executable substitutions in the arguments are operations too.
	for _, w := range call.Args {
		a.walkWordExecutables(w, depth)
	}
	a.addOperation(argv, depth)
}

func (a *shellAnalyzer) addOperation(argv []ShellWord, depth int) {
	if len(argv) == 0 {
		return
	}
	exe := argv[0]
	name := exe.Value
	if !exe.Known {
		name = exe.Text
	}
	base := lastPathSegment(name)

	// Transparent wrappers: consume their own flags and recurse into the
	// payload they execute.
	if payload, ok := wrapperPayload(base, argv[1:]); ok {
		a.addOperation(payload, depth+1)
		return
	}
	if base == "xargs" {
		if payload, ok := xargsCommandPayload(argv[1:]); ok {
			a.addOperation(payload, depth+1)
			return
		}
	}
	if base == "find" {
		for _, payload := range findPayloads(argv[1:]) {
			a.addOperation(payload, depth+1)
		}
	}

	op := ShellOperation{
		ID:          a.nextID,
		Executable:  exe,
		Argv:        argv,
		Directories: []string{a.cwd},
	}
	a.nextID++
	op.Flags, op.Operands = splitFlags(argv[1:])
	a.ops = append(a.ops, op)
	if base == "cd" {
		if next, ok := cdTarget(&op, a.cwd); ok {
			a.cwd = next
		}
	}
}

// wrapperPayload returns the argv a transparent shell wrapper executes.
func wrapperPayload(base string, args []ShellWord) ([]ShellWord, bool) {
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh":
		for i := 0; i < len(args); i++ {
			v := args[i].Value
			if !args[i].Known {
				return nil, false
			}
			if v == "-c" || (strings.HasPrefix(v, "-") && strings.Contains(v, "c")) {
				if i+1 < len(args) {
					return splitCommandWord(args[i+1]), true
				}
				return nil, false
			}
		}
		return nil, false
	case "eval":
		if len(args) == 0 {
			return nil, false
		}
		var parts []string
		for _, a := range args {
			if !a.Known {
				return nil, false
			}
			parts = append(parts, a.Value)
		}
		return splitCommandWord(ShellWord{Text: strings.Join(parts, " "), Value: strings.Join(parts, " "), Known: true, SingleField: true}), true
	}
	return nil, false
}

// splitCommandWord parses a payload string into argv words.
func splitCommandWord(w ShellWord) []ShellWord {
	if !w.Known {
		return []ShellWord{w}
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(w.Value), "")
	if err != nil {
		return []ShellWord{w}
	}
	var out []ShellWord
	for _, stmt := range f.Stmts {
		call, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			continue
		}
		for _, arg := range call.Args {
			out = append(out, decodeWord(arg, nil))
		}
	}
	if len(out) == 0 {
		return []ShellWord{w}
	}
	return out
}

// xargsCommandPayload returns the command xargs runs.
func xargsCommandPayload(args []ShellWord) ([]ShellWord, bool) {
	i := 0
	for i < len(args) {
		v := args[i].Value
		if !args[i].Known {
			return nil, false
		}
		if v == "--" {
			i++
			break
		}
		if !strings.HasPrefix(v, "-") || v == "-" {
			break
		}
		// Flags that consume a value.
		if xargsFlagConsumesValue(v) && i+1 < len(args) {
			i += 2
			continue
		}
		i++
	}
	if i >= len(args) {
		return nil, false
	}
	return args[i:], true
}

func xargsFlagConsumesValue(flag string) bool {
	switch flag {
	case "-n", "-I", "-P", "-L", "-s", "-d", "-E", "-a":
		return true
	}
	return false
}

// findPayloads returns every -exec/-execdir/-ok payload.
func findPayloads(args []ShellWord) [][]ShellWord {
	var out [][]ShellWord
	for i := 0; i < len(args); i++ {
		v := args[i].Value
		if !args[i].Known {
			continue
		}
		switch v {
		case "-exec", "-execdir", "-ok", "-okdir":
			var payload []ShellWord
			j := i + 1
			for j < len(args) {
				if args[j].Known && (args[j].Value == ";" || args[j].Value == "+") {
					break
				}
				payload = append(payload, args[j])
				j++
			}
			if len(payload) > 0 {
				out = append(out, payload)
			}
			i = j
		}
	}
	return out
}

// splitFlags separates leading flags from operands.
func splitFlags(args []ShellWord) ([]ShellFlag, []ShellWord) {
	var flags []ShellFlag
	var operands []ShellWord
	for _, a := range args {
		v := a.Value
		if a.Known && strings.HasPrefix(v, "-") && v != "-" && v != "--" {
			name, value, hasValue := strings.Cut(v, "=")
			flags = append(flags, ShellFlag{Name: name, Value: ShellWord{Text: value, Value: value, Known: a.Known, SingleField: true}, HasValue: hasValue})
			continue
		}
		operands = append(operands, a)
	}
	return flags, operands
}

// decodeWord decodes one word with a quote-aware decoder. It never executes
// expansion callbacks; environment values come only from the captured map.
func (a *shellAnalyzer) decodeWord(w *syntax.Word) ShellWord {
	return decodeWord(w, a.in.Env)
}

func decodeWord(w *syntax.Word, env map[string]string) ShellWord {
	if w == nil {
		return ShellWord{}
	}
	var printed, value strings.Builder
	syntax.NewPrinter().Print(&printed, w)
	known := true
	single := true
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			value.WriteString(unescapeLit(p.Value))
		case *syntax.SglQuoted:
			value.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				switch d := dp.(type) {
				case *syntax.Lit:
					value.WriteString(unescapeLit(d.Value))
				case *syntax.ParamExp:
					if v, ok := env[d.Param.Value]; ok {
						value.WriteString(v)
					} else {
						known = false
					}
				default:
					known = false
				}
			}
		case *syntax.ParamExp:
			if v, ok := env[p.Param.Value]; ok {
				value.WriteString(v)
			} else {
				known = false
			}
		case *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst, *syntax.ExtGlob:
			known = false
		default:
			known = false
		}
	}
	// An unquoted glob or an unquoted expansion can split into several fields.
	if !isQuoted(w) {
		single = false
	}
	return ShellWord{Text: printed.String(), Value: value.String(), Known: known, SingleField: single}
}

// isQuoted reports whether every part of the word is quoted.
func isQuoted(w *syntax.Word) bool {
	if w == nil || len(w.Parts) == 0 {
		return false
	}
	for _, part := range w.Parts {
		switch part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
		default:
			return false
		}
	}
	return true
}

// lastPathSegment returns the final path component of a command name.
func lastPathSegment(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}
