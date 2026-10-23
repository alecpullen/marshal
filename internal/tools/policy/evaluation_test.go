package policy

import (
	"errors"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func evalShell(t *testing.T, pe *PolicyEngine, state interaction.State, cmd string) Evaluation {
	t.Helper()
	a, err := AnalyzeShell(ShellInput{Command: cmd, WorkspaceRoot: "/repo", Dir: "/repo"})
	if err != nil {
		t.Fatalf("AnalyzeShell(%q): %v", cmd, err)
	}
	ev, err := pe.EvaluateInput(Input{ToolName: "shell.run", ActorState: state, Shell: &a})
	if err != nil {
		t.Fatalf("EvaluateInput(%q): %v", cmd, err)
	}
	return ev
}

func TestInteractionPolicyMatrix(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	cases := []struct {
		name  string
		state interaction.State
		cmd   string
		want  Decision
	}{
		{"ask read", interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "cat x", DecisionAllow},
		{"ask single-file removal", interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "rm notes.txt", DecisionDeny},
		{"ask disk utility", interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "diskutil list", DecisionDeny},
		{"ask redirect write", interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "cat x > out.txt", DecisionDeny},
		{"ask write", interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "git reset --hard", DecisionDeny},
		{"act read", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "cat x", DecisionAllow},
		{"act destructive", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "git reset --hard", DecisionConfirm},
		{"act auto destructive", interaction.State{Mode: interaction.ModeAct, Auto: true, Revision: 1}, "git reset --hard", DecisionAllow},
		{"act publish", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "git push", DecisionConfirm},
		{"act catastrophic", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "mkfs.ext4 /dev/sda", DecisionDeny},
	}
	for _, tc := range cases {
		if got := evalShell(t, pe, tc.state, tc.cmd).Decision; got != tc.want {
			t.Fatalf("%s: decision = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestShellEvaluationDeniesEveryHiddenCommand covers the forms whose nested
// command the analyzer used to skip: they must never reach Allow or Confirm.
func TestShellEvaluationDeniesEveryHiddenCommand(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	cmds := []string{
		`if rm -rf ~; then echo ok; fi`,
		`for f in $(rm -rf ~); do echo $f; done`,
		`case $(rm -rf ~) in *) ;; esac`,
		`[[ $(rm -rf ~) ]]`,
		`(( $(rm -rf ~) ))`,
		`let x=$(rm -rf ~)`,
		`X=$(rm -rf ~)`,
		`export X=$(rm -rf ~)`,
		`local X=$(rm -rf ~)`,
		`declare X=$(rm -rf ~)`,
		`readonly X=$(rm -rf ~)`,
		`echo ${X:-$(rm -rf ~)}`,
		`echo $(( $(rm -rf ~) ))`,
		`while rm -rf ~; do break; done`,
		`until rm -rf ~; do break; done`,
	}
	for _, cmd := range cmds {
		for _, state := range []interaction.State{ask, act} {
			if got := evalShell(t, pe, state, cmd).Decision; got != DecisionDeny {
				t.Fatalf("%q in %s: decision = %q, want deny", cmd, state.Mode, got)
			}
		}
	}

	if got := evalShell(t, pe, act, `rm -rf ~`).Decision; got != DecisionDeny {
		t.Fatalf("bare catastrophic: decision = %q, want deny", got)
	}
}

// TestShellEvaluationRedirectOnlyWriteIsAWrite covers a statement whose only
// act is a redirect: Ask denies the write, Act allows it.
func TestShellEvaluationRedirectOnlyWriteIsAWrite(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}
	for _, cmd := range []string{`> important.txt`, `>> log.txt`} {
		if got := evalShell(t, pe, ask, cmd).Decision; got != DecisionDeny {
			t.Fatalf("%q in Ask: decision = %q, want deny", cmd, got)
		}
		ev := evalShell(t, pe, act, cmd)
		if ev.Decision != DecisionAllow {
			t.Fatalf("%q in Act: decision = %q, want allow", cmd, ev.Decision)
		}
	}
}

// TestShellEvaluationNeverAllowsAnEmptyAnalysis covers fail-closed behavior: a
// parsed command with zero operations is not a proven read. A command with no
// statements at all (empty, comments only) stays a no-op.
func TestShellEvaluationNeverAllowsAnEmptyAnalysis(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}

	// An input redirect runs no command the analyzer understands.
	if got := evalShell(t, pe, act, `< important.txt`); got.Decision != DecisionConfirm {
		t.Fatalf("zero ops in Act: decision = %q, want confirm", got.Decision)
	} else if !got.ExactCommandApproval {
		t.Fatal("zero ops in Act: confirmation did not require exact approval")
	}
	if got := evalShell(t, pe, ask, `< important.txt`).Decision; got != DecisionDeny {
		t.Fatalf("zero ops in Ask: decision = %q, want deny", got)
	}

	// Nothing to run is still nothing to approve.
	for _, cmd := range []string{"", "# only a comment"} {
		if got := evalShell(t, pe, act, cmd).Decision; got != DecisionAllow {
			t.Fatalf("%q: decision = %q, want allow", cmd, got)
		}
	}
}

func TestF4AllowCannotPreauthorizeDestructive(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	cfg.Permissions.Rules = []config.PermissionRule{
		{Permission: "shell", Pattern: "*", Action: "allow"},
	}
	pe := NewEngine(&cfg, []string{})

	// A destructive operation outside the workspace keeps its System
	// requirement even when an F4 allow rule matches it.
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "rm -rf /tmp/x")
	if ev.Decision != DecisionConfirm {
		t.Fatalf("decision = %q, want confirm despite F4 allow", ev.Decision)
	}
	if !ev.Required.System {
		t.Fatal("F4 allow rule lifted the outside-workspace System requirement")
	}

	// The same rule still preauthorizes a non-destructive operation, so the
	// gate moved but the rule was not disabled.
	ev = evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "touch x")
	if ev.Decision != DecisionAllow {
		t.Fatalf("touch decision = %q, want allow by F4 rule", ev.Decision)
	}
}

func TestAskCeilingPrecedesEveryAllow(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	cfg.Tools.Shell.Allow.Commands = []string{"git"}
	pe := NewEngine(&cfg, nil)
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAsk, Revision: 1}, "git reset --hard")
	if ev.Decision != DecisionDeny {
		t.Fatalf("Ask + allow decision = %q, want deny", ev.Decision)
	}
	if !ev.Required.Act {
		t.Fatal("Ask denial did not require Act")
	}
}

func TestShellRulesMatchOperationsAndPreserveExplicitPatterns(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = []string{"danger"}
	pe := NewEngine(&cfg, nil)
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "danger x")
	if ev.Decision != DecisionDeny {
		t.Fatalf("deny rule decision = %q, want deny", ev.Decision)
	}
}

func TestShellConfirmAndDenyCannotBeMasked(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = []string{"danger"}
	cfg.Tools.Shell.Allow.Commands = []string{"danger"}
	pe := NewEngine(&cfg, nil)
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "danger x")
	if ev.Decision != DecisionDeny {
		t.Fatalf("deny masked by allow: decision = %q", ev.Decision)
	}
}

func TestClonedEngineReadsCurrentConfigSource(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	rev := uint64(1)
	pe.SetConfigSource(func() (config.Config, []string, uint64) { return cfg, nil, rev })
	clone := pe.Clone()
	rev = 2
	_, _, got := clone.snapshot()
	if got != 2 {
		t.Fatalf("clone source revision = %d, want 2", got)
	}
}

func TestEvaluationRejectsMismatchedSourceRevision(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	pe.SetConfigSource(func() (config.Config, []string, uint64) { return cfg, nil, 2 })
	a, err := AnalyzeShell(ShellInput{Command: "cat x", WorkspaceRoot: "/repo", Dir: "/repo"})
	if err != nil {
		t.Fatalf("AnalyzeShell: %v", err)
	}
	_, err = pe.EvaluateInput(Input{ToolName: "shell.run", ActorState: interaction.State{Mode: interaction.ModeAct, Revision: 1}, Shell: &a})
	if !errors.Is(err, ErrStalePolicy) {
		t.Fatalf("EvaluateInput = %v, want ErrStalePolicy", err)
	}
}

func TestAskNetworkAndMCPRequirePositiveReadClassification(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	// With no registry, an unclassified tool is treated as read-only.
	ev, err := pe.EvaluateInput(Input{ToolName: "web.fetch", ActorState: interaction.State{Mode: interaction.ModeAsk, Revision: 1}})
	if err != nil {
		t.Fatalf("EvaluateInput: %v", err)
	}
	if ev.Decision != DecisionAllow {
		t.Fatalf("unclassified tool in Ask = %q, want allow", ev.Decision)
	}
}

func TestShellApprovedDestructionHasNoSecondGuardrailDeny(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Revision: 1}, "git reset --hard")
	if ev.Decision != DecisionConfirm {
		t.Fatalf("approved destruction = %q, want confirm", ev.Decision)
	}
	if !ev.ExactCommandApproval {
		t.Fatal("destructive confirmation did not require exact approval")
	}
}
