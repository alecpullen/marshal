package sddreview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/theme"
	"marshal/internal/pipeline"
	"marshal/internal/sddauthor"
)

var testTheme = theme.LoadFor(false, "xterm-256color")

func init() {
	theme.Reload(testTheme)
}

func inspectionFixture() *pipeline.Inspection {
	return &pipeline.Inspection{
		Plan: &pipeline.Plan{Path: "/repo/.marshal/plans/feature.md", Slug: "feature"},
		IR: &pipeline.PlanIR{
			Tasks: []pipeline.TaskIR{
				{TaskN: 1, Title: "Add file", Operations: []pipeline.Operation{&pipeline.FileOp{Path: "created.txt"}}},
			},
		},
		Report: pipeline.CompileReport{
			Tasks: []pipeline.TaskReport{{TaskN: 1, Title: "Add file", DetOps: 1, EstCalls: 0}},
			Total: struct {
				DetOps   int
				AgentOps int
				EstCalls int
			}{DetOps: 1, AgentOps: 0, EstCalls: 0},
		},
		EffectiveStrategy: pipeline.StrategyAdaptive,
		HasMarshalBlocks:  true,
	}
}

func TestPanelRendersCandidateAndMetadata(t *testing.T) {
	p := New(sddauthor.Result{Request: sddauthor.Request{PlanPath: "/repo/.marshal/plans/feature.md"}, Inspection: inspectionFixture()})
	out := ansi.Strip(p.View(80, 20))
	for _, want := range []string{"feature.md", "1 task", "deterministic", "1", "adaptive"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestEnterEmitsAcceptWhenNoErrors(t *testing.T) {
	p := New(sddauthor.Result{Inspection: inspectionFixture()})
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command when inspection has no errors")
	}
	msg := cmd()
	if _, ok := msg.(AcceptMsg); !ok {
		t.Fatalf("msg = %T, want AcceptMsg", msg)
	}
}

func TestEnterEmitsNoCommandWhenErrorsExist(t *testing.T) {
	insp := inspectionFixture()
	insp.Diagnostics = []pipeline.Diagnostic{{Severity: pipeline.DiagError, Message: "blocked"}}
	p := New(sddauthor.Result{Inspection: insp})
	if cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("Enter must not emit a command when inspection has errors")
	}
}

func TestEscEmitsCancel(t *testing.T) {
	p := New(sddauthor.Result{Inspection: inspectionFixture()})
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc should emit a command")
	}
	msg := cmd()
	if _, ok := msg.(CancelMsg); !ok {
		t.Fatalf("msg = %T, want CancelMsg", msg)
	}
}

func TestPanelNeverStartsRunner(t *testing.T) {
	// The panel has no reference to a runner; it only emits AcceptMsg/CancelMsg.
	p := New(sddauthor.Result{Inspection: inspectionFixture()})
	cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should emit a command")
	}
	if _, ok := cmd().(AcceptMsg); !ok {
		t.Fatalf("panel must emit AcceptMsg, not start a runner")
	}
}
