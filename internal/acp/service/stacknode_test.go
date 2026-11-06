package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

func TestStackNodeReturnsACallsDetail(t *testing.T) {
	st := newState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{Model: "qwen3"})
	long := strings.Repeat("x", acpwire.WireTextCap*3)
	ev := registry.AuditEvent{
		Timestamp: time.Unix(2, 0), ToolName: "shell.run", StepID: step,
		ToolCallID: "c1", ResultContent: long,
	}
	st.LogToolCall(ev)

	_, sess := New(Deps{State: st})
	res, err := sess.StackNode(context.Background(), viewmodel.ToolKey(ev), 0)
	if err != nil {
		t.Fatalf("StackNode(%q) = %v", viewmodel.ToolKey(ev), err)
	}
	if len(res.Detail.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(res.Detail.Calls))
	}
	if got := res.Detail.Calls[0].Output; got != long {
		t.Fatalf("output = %d bytes, want the whole %d-byte output", len(got), len(long))
	}
	if want := fmt.Sprintf("step:%d", step); res.Node.Parent != want {
		t.Fatalf("node parent = %q, want the beat %q", res.Node.Parent, want)
	}
	if res.Detail.Step == nil {
		t.Fatal("the call row carries no step")
	}
	if res.Detail.Step.ID != int64(step) {
		t.Fatalf("step id = %d, want %d", res.Detail.Step.ID, step)
	}
}

func TestStackNodeReadsASubagentsTranscript(t *testing.T) {
	parent := newState(t, "parent")

	child := newState(t, "child")
	child.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := child.BeginStep(session.Actor{Model: "qwen3"})
	ev := registry.AuditEvent{
		Timestamp: time.Unix(20, 0), ToolName: "shell.run", StepID: step,
		ToolCallID: "c1", ResultSummary: "ok",
	}
	child.LogToolCall(ev)
	view := parent.RegisterSubagent("scout", child)

	_, sess := New(Deps{State: parent})
	key := viewmodel.ToolKey(ev)
	res, err := sess.StackNode(context.Background(), key, view.ID)
	if err != nil {
		t.Fatalf("StackNode(%q, %d) = %v", key, view.ID, err)
	}
	if len(res.Detail.Calls) != 1 || res.Detail.Calls[0].CallID != "c1" {
		t.Fatalf("calls = %+v, want the child's c1", res.Detail.Calls)
	}
	if _, err := sess.StackNode(context.Background(), key, 0); err == nil {
		t.Fatalf("StackNode(%q, 0) = nil error, want a miss: the parent holds no such node", key)
	}
}

func TestStackNodeFailsForAnUnknownNodeOrSubagent(t *testing.T) {
	st := newState(t, "s1")
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := st.BeginStep(session.Actor{Model: "qwen3"})
	st.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(2, 0), ToolName: "shell.run",
		StepID: step, ToolCallID: "c1", ResultSummary: "ok"})

	_, sess := New(Deps{State: st})
	if _, err := sess.StackNode(context.Background(), "tool:999:c9", 0); err == nil {
		t.Fatal(`StackNode("tool:999:c9", 0) = nil error, want a miss`)
	}
	if _, err := sess.StackNode(context.Background(), "tool:999:c9", 999999); err == nil {
		t.Fatal("StackNode with subagent 999999 = nil error, want a miss")
	}
}

func TestStackNodeOnAReplacedSessionFails(t *testing.T) {
	h, old := New(Deps{State: newState(t, "s1")})
	h.swap(newState(t, "s2"))

	if _, err := old.StackNode(context.Background(), "step:1", 0); !acpclient.IsCode(err, acpwire.CodeSessionClosed) {
		t.Fatalf("StackNode on a closed session = %v, want the %q code", err, acpwire.CodeSessionClosed)
	}
}
