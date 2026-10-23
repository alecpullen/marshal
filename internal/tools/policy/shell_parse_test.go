package policy

import (
	"strings"
	"testing"
)

func analyze(t *testing.T, cmd string) ShellAnalysis {
	t.Helper()
	a, err := AnalyzeShell(ShellInput{Command: cmd})
	if err != nil {
		t.Fatalf("AnalyzeShell(%q): %v", cmd, err)
	}
	return a
}

func hasExecutable(a ShellAnalysis, name string) bool {
	for _, op := range a.Operations {
		if op.Executable.Value == name {
			return true
		}
	}
	return false
}

func TestShellParseLiteralDataNeverBecomesOperation(t *testing.T) {
	cmds := []string{
		`git commit -m 'sudo reboot mkfs git reset --hard'`,
		`git commit -m "sudo reboot" -m "mkfs.ext4 /dev/sda"`,
		`echo 'rm -rf /'`,
		`printf '%s\n' 'shutdown -h now'`,
	}
	for _, cmd := range cmds {
		a := analyze(t, cmd)
		for _, op := range a.Operations {
			switch op.Executable.Value {
			case "sudo", "reboot", "mkfs", "mkfs.ext4", "rm", "shutdown":
				t.Fatalf("%q: quoted data became operation %q", cmd, op.Executable.Value)
			}
		}
	}
}

func TestShellParseKnownEmptyAndUnknownWordsDiffer(t *testing.T) {
	a := analyze(t, `echo "" $UNSET`)
	if len(a.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(a.Operations))
	}
	argv := a.Operations[0].Argv
	if len(argv) != 3 {
		t.Fatalf("argv = %+v, want 3 words", argv)
	}
	if !argv[1].Known || argv[1].Value != "" {
		t.Fatalf("empty quoted word = %+v, want known empty", argv[1])
	}
	if argv[2].Known {
		t.Fatalf("unset expansion = %+v, want unknown", argv[2])
	}
}

func TestShellParseEveryNestedExecutable(t *testing.T) {
	cmds := []string{
		`sh -c 'rm -rf x'`,
		`bash -lc 'rm -rf x'`,
		`echo $(rm -rf x)`,
		"echo `rm -rf x`",
		`echo <(rm -rf x)`,
		`eval 'rm -rf x'`,
		`xargs rm`,
		`find . -exec rm {} \;`,
	}
	for _, cmd := range cmds {
		a := analyze(t, cmd)
		if !hasExecutable(a, "rm") {
			t.Fatalf("%q: no rm operation in %+v", cmd, a.Operations)
		}
	}
}

func TestShellParseWrapperFlagConsumption(t *testing.T) {
	a := analyze(t, `sh -lc 'rm -rf x'`)
	if len(a.Operations) == 0 || a.Operations[0].Executable.Value != "rm" {
		t.Fatalf("sh -lc payload = %+v, want rm", a.Operations)
	}

	a = analyze(t, `xargs -n 1 rm`)
	if len(a.Operations) == 0 || a.Operations[0].Executable.Value != "rm" {
		t.Fatalf("xargs -n 1 payload = %+v, want rm", a.Operations)
	}

	a = analyze(t, `find . -exec sh -c 'rm -rf x' \;`)
	if !hasExecutable(a, "rm") {
		t.Fatalf("find -exec sh -c payload = %+v, want rm", a.Operations)
	}
}

func TestShellParseHeredocExecutionContexts(t *testing.T) {
	// An unquoted heredoc expands its command substitutions.
	a := analyze(t, "cat <<EOF\n$(rm -rf x)\nEOF\n")
	if !hasExecutable(a, "rm") {
		t.Fatalf("unquoted heredoc did not expand: %+v", a.Operations)
	}

	// A quoted heredoc body is literal data.
	a = analyze(t, "cat <<'EOF'\n$(rm -rf x)\nEOF\n")
	if hasExecutable(a, "rm") {
		t.Fatalf("quoted heredoc executed its body: %+v", a.Operations)
	}
}

func TestShellParseAllFindPayloads(t *testing.T) {
	a := analyze(t, `find . -exec rm {} \; -exec sh -c 'rm -rf x' \;`)
	count := 0
	for _, op := range a.Operations {
		if op.Executable.Value == "rm" {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("rm operations = %d, want 2 in %+v", count, a.Operations)
	}
}

// hasCatastrophic reports whether the analysis contains a catastrophic
// operation, whatever else it found.
func hasCatastrophic(a ShellAnalysis) bool {
	for _, op := range a.Operations {
		if op.Effects&ShellCatastrophic != 0 {
			return true
		}
	}
	return false
}

// TestShellParseWalksEveryExecutablePosition covers the constructs whose
// commands the analyzer used to skip: conditions, loop words, case subjects and
// patterns, extended tests, arithmetic, let, declaration builtins and bare
// assignments. Each of these really runs the nested command.
func TestShellParseWalksEveryExecutablePosition(t *testing.T) {
	cmds := []string{
		`rm -rf ~`,
		`if rm -rf ~; then echo ok; fi`,
		`if true; then echo ok; elif rm -rf ~; then :; fi`,
		`for f in $(rm -rf ~); do echo $f; done`,
		`for ((i=$(rm -rf ~); i < 1; i++)); do break; done`,
		`case $(rm -rf ~) in *) ;; esac`,
		`case x in $(rm -rf ~)) ;; esac`,
		`[[ $(rm -rf ~) ]]`,
		`[[ a == b && $(rm -rf ~) ]]`,
		`(( $(rm -rf ~) ))`,
		`let x=$(rm -rf ~)`,
		`X=$(rm -rf ~)`,
		`X=$(rm -rf ~) echo ok`,
		`export X=$(rm -rf ~)`,
		`local X=$(rm -rf ~)`,
		`declare X=$(rm -rf ~)`,
		`readonly X=$(rm -rf ~)`,
		`echo ${X:-$(rm -rf ~)}`,
		`echo ${X/$(rm -rf ~)/y}`,
		`echo $(( $(rm -rf ~) ))`,
		`while rm -rf ~; do break; done`,
		`until rm -rf ~; do break; done`,
	}
	for _, cmd := range cmds {
		if a := analyze(t, cmd); !hasCatastrophic(a) {
			t.Fatalf("%q: no catastrophic operation in %+v", cmd, a.Operations)
		}
	}
}

// TestShellParseKeepsWalksThatAlreadyWorked guards the forms that were already
// analyzed correctly.
func TestShellParseKeepsWalksThatAlreadyWorked(t *testing.T) {
	cmds := []string{
		`! rm -rf ~`,
		`{ rm -rf ~; }`,
		`rm -rf ~ &`,
		`f() { rm -rf ~; }; f`,
		`echo ok | rm -rf ~`,
		`bash -c 'rm -rf ~'`,
		`eval 'rm -rf ~'`,
		`time rm -rf ~`,
		`cat <<< $(rm -rf ~)`,
	}
	for _, cmd := range cmds {
		if a := analyze(t, cmd); !hasCatastrophic(a) {
			t.Fatalf("%q: no catastrophic operation in %+v", cmd, a.Operations)
		}
	}
}

// TestShellParseRedirectOnlyStatementIsAWrite covers a statement whose only
// act is a redirect: it writes its target, so it must not analyze empty.
func TestShellParseRedirectOnlyStatementIsAWrite(t *testing.T) {
	cases := []struct{ cmd, target string }{
		{`> important.txt`, "important.txt"},
		{`>> log.txt`, "log.txt"},
		{`2> errors.txt`, "errors.txt"},
	}
	for _, tc := range cases {
		a := analyze(t, tc.cmd)
		if len(a.Operations) != 1 {
			t.Fatalf("%q: operations = %+v, want one write", tc.cmd, a.Operations)
		}
		op := a.Operations[0]
		if op.Effects&ShellWrite == 0 {
			t.Fatalf("%q: effects = %b, want write", tc.cmd, op.Effects)
		}
		if op.Effects&ShellUnknown != 0 {
			t.Fatalf("%q: effects = %b, want no unknown", tc.cmd, op.Effects)
		}
		if len(op.Operands) != 1 || op.Operands[0].Value != tc.target {
			t.Fatalf("%q: operands = %+v, want target %q", tc.cmd, op.Operands, tc.target)
		}
	}
}

func TestShellParseInvalidSyntaxAndControls(t *testing.T) {
	if _, err := AnalyzeShell(ShellInput{Command: `echo "unterminated`}); err == nil {
		t.Fatal("invalid syntax was accepted")
	}
	if _, err := AnalyzeShell(ShellInput{Command: "echo \x01"}); err == nil {
		t.Fatal("a display control character was accepted")
	}
	if _, err := AnalyzeShell(ShellInput{Command: "echo ok"}); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	if !strings.Contains("ok", "ok") {
		t.Fatal("unreachable")
	}
}
