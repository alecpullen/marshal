package policy

import (
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
)

func TestActRunsOperationsWithoutImplicitApproval(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	for _, command := range []string{
		"git checkout feature", "git switch -c feature", "git checkout -- main.go",
		"git reset --hard", "git push origin main", "rm -rf build",
		"docker compose up -d", "python3 scripts/build.py",
	} {
		t.Run(command, func(t *testing.T) {
			ev := evalShell(t, pe, actState(), command)
			if ev.Decision != DecisionAllow || ev.ExactCommandApproval {
				t.Fatalf("Act evaluation = %+v, want allow without approval", ev)
			}
		})
	}
	for _, tool := range []string{"web.fetch", "mcp.example.run"} {
		pe := actEditEngine(t)
		ev, err := pe.EvaluateInput(Input{ToolName: tool, ActorState: actState(), WorkspaceRoot: "/repo"})
		if err != nil || ev.Decision != DecisionAllow {
			t.Errorf("%s = %+v, %v, want allow", tool, ev, err)
		}
	}
}

func TestFullAutoPreapprovesExplicitConfirmButPreservesDeny(t *testing.T) {
	for _, action := range []string{"ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			cfg := config.Default()
			cfg.Tools.Shell.Deny.Patterns = nil
			cfg.Permissions.Rules = []config.PermissionRule{{Permission: "shell", Pattern: "*", Action: action}}
			pe := NewEngine(&cfg, nil)
			state := interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 1}
			for _, command := range []string{"git push", "docker compose up", "rm -rf build"} {
				ev := evalShell(t, pe, state, command)
				want := DecisionAllow
				if action == "deny" {
					want = DecisionDeny
				}
				if ev.Decision != want || ev.ExactCommandApproval {
					t.Errorf("%s = %+v, want %s", command, ev, want)
				}
			}
		})
	}
}

func TestActHonorsExplicitConfirmation(t *testing.T) {
	cfg := config.Default()
	cfg.Permissions.Rules = []config.PermissionRule{{Permission: "shell", Pattern: "*", Action: "ask"}}
	pe := NewEngine(&cfg, nil)
	if ev := evalShell(t, pe, actState(), "git checkout feature"); ev.Decision != DecisionConfirm {
		t.Fatalf("explicit ask in Act = %+v, want confirm", ev)
	}
}

func TestPreapprovalCannotHideSystemRequirement(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.FloorCommands = []string{"npm publish"}
	pe := NewEngine(&cfg, nil)
	for _, auto := range []bool{false, true} {
		for _, command := range []string{"git push; npm install -g tool", "npm publish; go clean -cache", "git reset --hard; npm install -g tool"} {
			ev := evalShell(t, pe, interaction.State{Mode: interaction.ModeAct, Auto: auto, Revision: 1}, command)
			want := DecisionConfirm
			if auto {
				want = DecisionDeny
			} // A bounded child cannot request a grant.
			if ev.Decision != want || !ev.Required.System {
				t.Errorf("auto=%v %q = %+v, want System requirement", auto, command, ev)
			}
		}
	}
	a := analyze(t, "id")
	ev, err := pe.EvaluateInput(Input{ToolName: "shell.sudo", ActorState: interaction.State{Mode: interaction.ModeAct, Auto: true, Revision: 1}, Shell: &a})
	if err != nil || ev.Decision != DecisionDeny || !ev.Required.System {
		t.Fatalf("bounded sudo = %+v, %v, want missing System denial", ev, err)
	}
}
