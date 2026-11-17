package policy

import (
	"errors"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/permissions"
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
		{"act destructive", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "git reset --hard", DecisionAllow},
		{"act auto destructive", interaction.State{Mode: interaction.ModeAct, Auto: true, Revision: 1}, "git reset --hard", DecisionAllow},
		{"act publish", interaction.State{Mode: interaction.ModeAct, Revision: 1}, "git push", DecisionAllow},
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
	if got := evalShell(t, pe, act, `< important.txt`); got.Decision != DecisionAllow {
		t.Fatalf("zero ops in Act: decision = %q, want allow", got.Decision)
	} else if got.ExactCommandApproval {
		t.Fatal("Act should not require implicit command approval")
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
	pe.SetConfigSource(func() (config.Config, []string, []session.PermissionGrant, uint64) { return cfg, nil, nil, rev })
	clone := pe.Clone()
	rev = 2
	_, _, _, got := clone.snapshot()
	if got != 2 {
		t.Fatalf("clone source revision = %d, want 2", got)
	}
}

func TestEvaluationRejectsMismatchedSourceRevision(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	pe.SetConfigSource(func() (config.Config, []string, []session.PermissionGrant, uint64) { return cfg, nil, nil, 2 })
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
	if ev.Decision != DecisionAllow {
		t.Fatalf("approved destruction = %q, want allow", ev.Decision)
	}
	if ev.ExactCommandApproval {
		t.Fatal("Act unexpectedly required an exact approval")
	}
}

// TestShellPolicyWrappedCompoundPayloadsFailClosed covers the AUD-01 policy
// half: a payload with any statement that is not provably read-only can never
// be admitted as read-only, whatever its first statement looks like.
func TestShellPolicyWrappedCompoundPayloadsFailClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}

	for _, cmd := range []string{
		"sh -c 'echo ok; touch x'",
		"sh -c 'echo ok; sudo rm x'",
		"eval 'echo ok; rm -rf x'",
		"sh -c 'echo ok > out.txt'",
		"sh -c 'echo ok; npm publish'",
	} {
		if got := evalShell(t, pe, ask, cmd).Decision; got != DecisionDeny {
			t.Fatalf("%q in Ask: decision = %q, want deny", cmd, got)
		}
	}

	// The catastrophic and privileged statements behind a read are denied in
	// every mode.
	for _, cmd := range []string{
		"sh -c 'echo ok; rm -rf /'",
		"sh -c 'echo ok; sudo rm x'",
	} {
		if got := evalShell(t, pe, act, cmd).Decision; got != DecisionDeny {
			t.Fatalf("%q in Act: decision = %q, want deny", cmd, got)
		}
	}

	if got := evalShell(t, pe, act, "sh -c 'echo ok; touch x'").Decision; got != DecisionAllow {
		t.Fatalf("wrapped write in Act: decision = %q, want allow", got)
	}
	if got := evalShell(t, pe, ask, "sh -c 'echo ok'").Decision; got != DecisionAllow {
		t.Fatalf("wrapped read in Ask: decision = %q, want allow", got)
	}
}

// TestShellPolicyWrappedPayloadMatchesConfiguredFloor covers the second
// AUD-01 bypass: a configured floor prefix must match a statement inside a
// wrapped payload, not only the flattened argv's first word.
func TestShellPolicyWrappedPayloadMatchesConfiguredFloor(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	cfg.Tools.Shell.FloorCommands = []string{"npm publish"}
	pe := NewEngine(&cfg, nil)
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}

	for _, cmd := range []string{
		"sh -c 'echo ok; npm publish'",
		"eval 'echo ok; npm publish'",
	} {
		if ev := evalShell(t, pe, act, cmd); ev.Decision == DecisionAllow {
			t.Fatalf("%q: decision = %q, want the configured floor to apply", cmd, ev.Decision)
		}
	}
	if ev := evalShell(t, pe, act, "npm publish"); ev.Decision == DecisionAllow {
		t.Fatalf("bare floored command: decision = %q, want approval", ev.Decision)
	}
	if got := evalShell(t, pe, act, "sh -c 'echo ok'").Decision; got != DecisionAllow {
		t.Fatalf("wrapped read: decision = %q, want allow", got)
	}
}

// TestShellPolicyGitConfigMutationsFailClosed covers AUD-04's policy half.
func TestShellPolicyGitConfigMutationsFailClosed(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	act := interaction.State{Mode: interaction.ModeAct, Revision: 1}

	for _, cmd := range []string{
		"git config user.name x",
		"git config core.hooksPath /p",
		"git config --unset some.key",
		"git config --add some.key value",
		"git config --system user.name x",
	} {
		if got := evalShell(t, pe, ask, cmd).Decision; got != DecisionDeny {
			t.Fatalf("%q in Ask: decision = %q, want deny", cmd, got)
		}
	}

	for _, cmd := range []string{
		"git config --get user.name",
		"git config --list",
		"git config",
		"git status",
		"git diff",
		"cat file",
	} {
		if got := evalShell(t, pe, ask, cmd).Decision; got != DecisionAllow {
			t.Fatalf("%q in Ask: decision = %q, want allow", cmd, got)
		}
	}

	if ev := evalShell(t, pe, act, "git config --global user.name x"); ev.Decision == DecisionAllow || !ev.Required.System {
		t.Fatalf("global git config in Act = %+v, want a System requirement", ev)
	}
}

// captureAskRules is the configured rule an approval answer of "session" can
// lift: every capture target asks first.
func captureAskRules() []permissions.Rule {
	return []permissions.Rule{{Permission: "image.capture", Pattern: "*", Action: permissions.ActionAsk}}
}

// TestGrantLiftsConfiguredAsk covers the point of a session grant: the
// configured ask becomes an allow for the granted target only.
func TestGrantLiftsConfiguredAsk(t *testing.T) {
	grants := []session.PermissionGrant{{Permission: "image.capture", Pattern: "active_window"}}

	if got, matched := evaluateSubjects(captureAskRules(), grants, "image.capture", []string{"active_window"}); !matched || got != DecisionAllow {
		t.Fatalf("granted target = (%q, %v), want (allow, true)", got, matched)
	}
	if got, matched := evaluateSubjects(captureAskRules(), grants, "image.capture", []string{"screen"}); !matched || got != DecisionConfirm {
		t.Fatalf("ungranted target = (%q, %v), want (confirm, true)", got, matched)
	}
}

// TestConfigDenyBeatsGrant confirms the precedence rule: a session can grant,
// never deny, so a configured deny is untouched by any grant.
func TestConfigDenyBeatsGrant(t *testing.T) {
	rules := []permissions.Rule{{Permission: "image.capture", Pattern: "screen", Action: permissions.ActionDeny}}
	grants := []session.PermissionGrant{{Permission: "image.capture", Pattern: "screen"}}

	if got, matched := evaluateSubjects(rules, grants, "image.capture", []string{"screen"}); !matched || got != DecisionDeny {
		t.Fatalf("denied target = (%q, %v), want (deny, true)", got, matched)
	}
}

// TestGrantAloneAllows covers a target no rule mentions at all: the grant is
// the only reason the tool does not fall through to the secure default.
func TestGrantAloneAllows(t *testing.T) {
	grants := []session.PermissionGrant{{Permission: "image.capture", Pattern: "active_window"}}

	if got, matched := evaluateSubjects(nil, grants, "image.capture", []string{"active_window"}); !matched || got != DecisionAllow {
		t.Fatalf("grant alone = (%q, %v), want (allow, true)", got, matched)
	}
	if got, matched := evaluateSubjects(nil, grants, "image.capture", []string{"screen"}); matched {
		t.Fatalf("no rule for an ungranted target = (%q, true), want no match", got)
	}
}

// TestNoGrantKeepsConfirm is the unchanged behaviour: an ask stays a confirm.
func TestNoGrantKeepsConfirm(t *testing.T) {
	if got, matched := evaluateSubjects(captureAskRules(), nil, "image.capture", []string{"active_window"}); !matched || got != DecisionConfirm {
		t.Fatalf("ask without a grant = (%q, %v), want (confirm, true)", got, matched)
	}
}

// TestDefaultCaptureRuleConfirmsBothTargets records that the shipped default
// prompts for every capture target until a session grant lifts it.
func TestDefaultCaptureRuleConfirmsBothTargets(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	pe.mu.RLock()
	rules := pe.rules
	pe.mu.RUnlock()

	for _, target := range []string{"active_window", "screen"} {
		if got, matched := evaluateSubjects(rules, nil, "image.capture", []string{target}); !matched || got != DecisionConfirm {
			t.Fatalf("default rule for %q = (%q, %v), want (confirm, true)", target, got, matched)
		}
	}
}
