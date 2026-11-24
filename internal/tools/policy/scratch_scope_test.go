// internal/tools/policy/scratch_scope_test.go — scratch roots are ordinary
// working space for the shell surface too.
package policy

import (
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

// evalShellWithScratch evaluates one shell input against the policy engine.
func evalShellWithScratch(t *testing.T, pe *PolicyEngine, state interaction.State, in ShellInput) Evaluation {
	t.Helper()
	a, err := AnalyzeShell(in)
	if err != nil {
		t.Fatalf("AnalyzeShell(%q): %v", in.Command, err)
	}
	ev, err := pe.EvaluateInput(Input{ToolName: "shell.run", ActorState: state, Shell: &a})
	if err != nil {
		t.Fatalf("EvaluateInput(%q): %v", in.Command, err)
	}
	return ev
}

// scratchFixture returns a workspace, a scratch root and a home directory laid
// out inside one temporary tree, so the test means the same thing on every
// platform instead of spelling POSIX paths.
func scratchFixture(t *testing.T) (workspace, scratch, home string) {
	t.Helper()
	base := t.TempDir()
	return filepath.Join(base, "repo"), filepath.Join(base, "scratch"), filepath.Join(base, "home")
}

// TestScratchRootIsNotAnExternalScope pins that a destructive operation
// scoped inside a scratch root is not an external operation: it does not need
// the System grant that only broad filesystem access warrants, and it is not
// denied. The approval tier it lands in is the destructive workspace one,
// which this test deliberately does not restate.
func TestScratchRootIsNotAnExternalScope(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	workspace, scratch, home := scratchFixture(t)

	ev := evalShellWithScratch(t, pe, act, ShellInput{
		Command:       "rm -rf " + filepath.Join(scratch, "build"),
		WorkspaceRoot: workspace,
		Dir:           workspace,
		ScratchRoots:  []string{scratch},
		HomeDir:       home,
	})
	if ev.Required.System {
		t.Fatal("a destructive operation inside a scratch root required System")
	}
	if ev.Decision == DecisionDeny {
		t.Fatal("a destructive operation inside a scratch root was denied")
	}

	// Outside every root, the same operation still needs System.
	ev = evalShellWithScratch(t, pe, act, ShellInput{
		Command:       "rm -rf " + filepath.Join(filepath.Dir(workspace), "elsewhere"),
		WorkspaceRoot: workspace,
		Dir:           workspace,
		ScratchRoots:  []string{scratch},
		HomeDir:       home,
	})
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
	workspace, scratch, _ := scratchFixture(t)
	home := filepath.Join(scratch, "home")

	ev := evalShellWithScratch(t, pe, act, ShellInput{
		Command:       "rm -rf " + filepath.Join(home, "notes"),
		WorkspaceRoot: workspace,
		Dir:           workspace,
		ScratchRoots:  []string{scratch},
		HomeDir:       home,
	})
	if !ev.Required.System {
		t.Fatal("a destructive operation inside the home directory did not require System")
	}
}

// TestScratchRootIsNotLaunderedAsAReadRoot pins the seam between the two root
// lists. The production wiring once listed every scratch root as a read root
// as well; the read-root loop then re-admitted the home directory the scratch
// loop had just withheld, and a destructive operation there needed no System.
func TestScratchRootIsNotLaunderedAsAReadRoot(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	workspace, scratch, _ := scratchFixture(t)
	home := filepath.Join(scratch, "home")

	ev := evalShellWithScratch(t, pe, act, ShellInput{
		Command:       "rm -rf " + filepath.Join(home, "notes"),
		WorkspaceRoot: workspace,
		Dir:           workspace,
		ScratchRoots:  []string{scratch},
		ReadRoots:     []string{scratch},
		HomeDir:       home,
	})
	if !ev.Required.System {
		t.Fatal("a scratch root listed as a read root let the home directory back in without System")
	}
}

// TestScratchPatternDoesNotInheritTheGrant pins that a scratch grant covers
// named work inside the root, not a pattern over everything in it: `rm -rf
// <scratch>/*` reaches past the session's staging area, and a pattern cannot
// be judged against the home-directory carve-out at all.
func TestScratchPatternDoesNotInheritTheGrant(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	workspace, scratch, home := scratchFixture(t)

	for _, command := range []string{
		"rm -rf " + filepath.Join(scratch, "*"),
		"rm -rf " + scratch,
	} {
		ev := evalShellWithScratch(t, pe, act, ShellInput{
			Command:       command,
			WorkspaceRoot: workspace,
			Dir:           workspace,
			ScratchRoots:  []string{scratch},
			HomeDir:       home,
		})
		if !ev.Required.System {
			t.Fatalf("%q reached a whole scratch root without System", command)
		}
	}
}
