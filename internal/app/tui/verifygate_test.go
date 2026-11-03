package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/commands"
	"marshal/internal/tools/registry"
)

func TestVerifyGateRowStates(t *testing.T) {
	cases := []struct {
		name        string
		build, test string
		wantWarn    bool
	}{
		{"configured", "make build", "make test", false},
		{"unconfigured", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.SDD.Verify = config.SDDVerifyConfig{Build: c.build, Test: c.test}
			row := verifyGateRow(cfg, "/tmp/nonexistent")
			if (row.Warn != "") != c.wantWarn {
				t.Errorf("%s: warn = %q, want warn=%v", c.name, row.Warn, c.wantWarn)
			}
		})
	}
}

func TestProposalPersistsOnApprove(t *testing.T) {
	workDir := t.TempDir()
	state := session.New(config.Default(), workDir, time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := testModelWith(inproc.Deps{
		State: state,
		VerifyProposal: func(context.Context) (acpwire.VerifyCommands, error) {
			return acpwire.VerifyCommands{Build: "make build", Test: "make test"}, nil
		},
	}, WithCommandRegistry(reg))
	m.resize(80, 24)

	// Open the /sdd preflight on a manifest-less repo so the gate is unknown.
	m.openRunPreflight("sdd", &fakeAgentRunner{called: make(chan string, 1)}, "plan.md")

	// Press f to dispatch the proposal.
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'f'})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("expected a proposal command from pressing f")
	}
	// Run the command to produce verifyProposeMsg.
	msg := cmd()
	mm, _ = m.Update(msg)
	m = mm.(Model)

	// The proposal should be stashed on the pending run.
	if m.pendingRun == nil {
		t.Fatal("pendingRun is nil after proposal")
	}
	if m.pendingRun.verifyBuild != "make build" || m.pendingRun.verifyTest != "make test" {
		t.Fatalf("proposal not stashed: %+v", m.pendingRun)
	}

	// Confirm the run; the proposed commands must be persisted to config.
	mm, _ = m.Update(castlist.StartMsg{})
	m = mm.(Model)

	data, err := os.ReadFile(config.ProjectConfigPath(workDir))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "make build") || !strings.Contains(s, "make test") {
		t.Fatalf("proposed commands not persisted:\n%s", s)
	}
}

func TestCastlistVerifyGateUnknown(t *testing.T) {
	p := castlist.New("t", []castlist.Row{{Title: "verify gate", Warn: "no command"}}, nil, "agent")
	if !p.VerifyGateUnknown() {
		t.Fatal("expected unknown gate")
	}
	p.SetVerifyRow("make build · make test")
	if p.VerifyGateUnknown() {
		t.Fatal("expected known gate after SetVerifyRow")
	}
}
