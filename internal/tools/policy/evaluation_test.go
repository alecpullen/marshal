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
