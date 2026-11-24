// internal/tools/policy/scratch_scope_test.go — scratch roots are ordinary
// working space for the shell surface too.
package policy

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// evalShellWithScratch evaluates one command against a workspace that grants
// scratch roots, and a home directory the scratch roots must not reach.
func evalShellWithScratch(t *testing.T, pe *PolicyEngine, state interaction.State, cmd string, scratch []string, home string) Evaluation {
	t.Helper()
	a, err := AnalyzeShell(ShellInput{
		Command:       cmd,
		WorkspaceRoot: "/repo",
		Dir:           "/repo",
		ScratchRoots:  scratch,
		HomeDir:       home,
	})
	if err != nil {
		t.Fatalf("AnalyzeShell(%q): %v", cmd, err)
	}
	ev, err := pe.EvaluateInput(Input{ToolName: "shell.run", ActorState: state, Shell: &a})
	if err != nil {
		t.Fatalf("EvaluateInput(%q): %v", cmd, err)
	}
	return ev
}

// TestScratchRootIsNotAnExternalScope pins that a destructive operation
// scoped inside a scratch root is a workspace operation: it needs the
// destructive-approval path, not the System grant that only broad filesystem
// access warrants.
func TestScratchRootIsNotAnExternalScope(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	scratch := []string{"/tmp/marshal-scratch"}

	ev := evalShellWithScratch(t, pe, act, "rm -rf /tmp/marshal-scratch/build", scratch, "/home/user")
	if ev.Required.System {
		t.Fatal("a destructive operation inside a scratch root required System")
	}
	if ev.Decision != DecisionAllow {
		t.Fatalf("decision = %v, want Allow", ev.Decision)
	}

	// Outside every root, the same operation still needs System.
	ev = evalShellWithScratch(t, pe, act, "rm -rf /etc/marshal-test", scratch, "/home/user")
	if !ev.Required.System {
		t.Fatal("a destructive operation outside every root did not require System")
	}
}

// TestScratchRootDoesNotCoverTheHomeDirectory pins that the home directory is
// never scratch space: a temporary directory that contains it must not become
// a way to run a destructive operation against the user's own files.
func TestScratchRootDoesNotCoverTheHomeDirectory(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	scratch := []string{"/tmp/marshal-scratch"}
	home := "/tmp/marshal-scratch/home"

	ev := evalShellWithScratch(t, pe, act, "rm -rf /tmp/marshal-scratch/home/notes", scratch, home)
	if !ev.Required.System {
		t.Fatal("a destructive operation inside the home directory did not require System")
	}
}
