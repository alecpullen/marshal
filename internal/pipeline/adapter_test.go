package pipeline

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/worktree"
)

func TestAdapterClearsProgressAfterRun(t *testing.T) {
	d, _ := scriptedDispatch(t, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	if err := a.Run(context.Background(), c.Plan.Path); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p := st.SDDProgress(); p.Active {
		t.Error("progress still active after a completed run — the live-strip spinner never stops")
	}
}

func TestAdapterKeepsProgressLiveAtGate(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	err := a.Run(context.Background(), c.Plan.Path)
	if err == nil {
		t.Fatal("Run: want the gate error, got nil")
	}
	if p := st.SDDProgress(); !p.Active {
		t.Error("progress cleared at a human gate — the run resumes after AnswerGate")
	}
}

func TestAdapterSurfacesGateAndAnswer(t *testing.T) {
	d, _ := scriptedDispatch(t, implAsks, implDone, reviewOK, implDone, reviewOK, reviewOK)
	c := testController(t, d, NewFakeCommandRunner())
	c.Git.(*worktree.FakeGitOps).Dirty = true
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	a := NewControllerAdapter(c, st)

	err := a.Run(context.Background(), c.Plan.Path)
	if err == nil {
		t.Fatal("Run: want the gate error, got nil")
	}
	gate := st.SDDGate()
	if gate.Question != "user level or system level?" {
		t.Errorf("gate = %+v, want the subagent's question", gate)
	}
	a.AnswerGate("User level.")
	if st.SDDGate().Question != "" {
		t.Error("gate not cleared after AnswerGate")
	}
	if err := a.Run(context.Background(), c.Plan.Path); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
}

func TestAdapterSatisfiesTheRunnerContract(t *testing.T) {
	// Compile-time shape check: the methods the TUI's AgentRunner needs.
	var a any = &ControllerAdapter{}
	if _, ok := a.(interface {
		Run(context.Context, string) error
		SetForceClass(string)
		SetPolicyRules([]config.PermissionRule)
		AnswerGate(string)
	}); !ok {
		t.Fatal("ControllerAdapter does not satisfy the TUI runner contract")
	}
}
