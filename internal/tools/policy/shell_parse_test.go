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
