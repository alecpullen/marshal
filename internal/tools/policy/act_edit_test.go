package policy

import (
	"context"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/interaction"
	"marshal/internal/tools/registry"
)

// actEditEngine builds an engine with the non-shell risks Act has to
// distinguish: an ordinary workspace write, a destructive tool, and a network
// tool.
func actEditEngine(t *testing.T) *PolicyEngine {
	t.Helper()
	reg := registry.New()
	h := func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}
	risks := map[string]registry.RiskLevel{
		"file.edit":  registry.RiskWorkspaceWrite,
		"file.write": registry.RiskWorkspaceWrite,
		"shell.sudo": registry.RiskDestructive,
		"web.fetch":  registry.RiskNetwork,
	}
	for name, risk := range risks {
		if err := reg.Register(registry.Tool{Name: name, Risk: risk, Handler: h}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	pe.WithRegistry(reg)
	return pe
}

func actState() interaction.State {
	return interaction.State{Mode: interaction.ModeAct, Revision: 1}
}

// TestActAllowsRoutineWorkspaceEdits pins the Act half of the policy matrix:
// routine workspace reads/edits are allowed without per-change approval.
// Asking on every edit is the retired Edit-mode behaviour.
func TestActAllowsRoutineWorkspaceEdits(t *testing.T) {
	pe := actEditEngine(t)
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"file.edit", map[string]any{"path": "internal/app/app.go", "old_string": "a", "new_string": "b"}},
		{"file.write", map[string]any{"path": "docs/notes.md", "content": "x"}},
		{"file.write", map[string]any{"path": "/repo/docs/notes.md", "content": "x"}},
	}
	for _, tc := range cases {
		ev, err := pe.EvaluateInput(Input{
			ToolName:      tc.tool,
			Args:          tc.args,
			ActorState:    actState(),
			WorkspaceRoot: "/repo",
		})
		if err != nil {
			t.Fatalf("EvaluateInput(%s): %v", tc.tool, err)
		}
		if ev.Decision != DecisionAllow {
			t.Errorf("%s %v in Act = %s, want allow", tc.tool, tc.args["path"], ev.Decision)
		}
	}
}

// TestActOutsideWorkspaceEditRequiresSystem pins the System column of the
// matrix: a routine edit outside the workspace needs System, and is allowed
// once it is granted.
func TestActOutsideWorkspaceEditRequiresSystem(t *testing.T) {
	pe := actEditEngine(t)
	args := map[string]any{"path": "/tmp/elsewhere/notes.md", "content": "x"}

	ev, err := pe.EvaluateInput(Input{ToolName: "file.write", Args: args, ActorState: actState(), WorkspaceRoot: "/repo"})
	if err != nil {
		t.Fatalf("EvaluateInput: %v", err)
	}
	if ev.Decision != DecisionConfirm {
		t.Fatalf("outside-workspace write in Act = %s, want confirm", ev.Decision)
	}
	if !ev.Required.System {
		t.Fatalf("outside-workspace write reasons = %+v, want the System requirement", ev.Reasons)
	}

	system := interaction.State{Mode: interaction.ModeAct, System: true, Revision: 1}
	ev, err = pe.EvaluateInput(Input{ToolName: "file.write", Args: args, ActorState: system, WorkspaceRoot: "/repo"})
	if err != nil {
		t.Fatalf("EvaluateInput: %v", err)
	}
	if ev.Decision != DecisionAllow {
		t.Fatalf("outside-workspace write in Act + System = %s, want allow", ev.Decision)
	}
}

// Act also preauthorizes network and destructive tools.
func TestActPreauthorizesNonRoutineTools(t *testing.T) {
	pe := actEditEngine(t)
	for _, tool := range []string{"shell.sudo", "web.fetch"} {
		ev, err := pe.EvaluateInput(Input{ToolName: tool, Args: map[string]any{"command": "id"}, ActorState: actState(), WorkspaceRoot: "/repo"})
		if err != nil {
			t.Fatalf("EvaluateInput(%s): %v", tool, err)
		}
		if ev.Decision != DecisionAllow {
			t.Errorf("%s in Act = %s, want allow", tool, ev.Decision)
		}
	}
}

// TestAskStillRefusesWorkspaceEdits pins Ask's read-only ceiling for the same
// tools, so relaxing Act does not widen Ask.
func TestAskStillRefusesWorkspaceEdits(t *testing.T) {
	pe := actEditEngine(t)
	ask := interaction.State{Mode: interaction.ModeAsk, Revision: 1}
	ev, err := pe.EvaluateInput(Input{
		ToolName:      "file.edit",
		Args:          map[string]any{"path": "internal/app/app.go", "old_string": "a", "new_string": "b"},
		ActorState:    ask,
		WorkspaceRoot: "/repo",
	})
	if err != nil {
		t.Fatalf("EvaluateInput: %v", err)
	}
	if ev.Decision != DecisionDeny {
		t.Fatalf("file.edit in Ask = %s, want deny", ev.Decision)
	}
	if !ev.Required.Act {
		t.Fatalf("file.edit in Ask reasons = %+v, want the Act requirement", ev.Reasons)
	}
}
