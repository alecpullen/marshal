package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// findKind returns the first node of kind k and its parent.
func findKind(turns []*Node, k Kind) (n, parent *Node) {
	var walk func(ns []*Node, p *Node) (*Node, *Node)
	walk = func(ns []*Node, p *Node) (*Node, *Node) {
		for _, c := range ns {
			if c.Kind == k {
				return c, p
			}
			if f, fp := walk(c.Children, c); f != nil {
				return f, fp
			}
		}
		return nil, nil
	}
	return walk(turns, nil)
}

func TestStackNodeOfFindsANodeAndItsParent(t *testing.T) {
	long := strings.Repeat("x", acpwire.WireTextCap*3)
	st := projTestState(t)
	st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	id := st.BeginStep(session.Actor{Model: "qwen3"})
	st.AddNarration(id, "Running the tests.")
	st.LogToolCall(registry.AuditEvent{
		Timestamp: at(2), ToolName: "shell.run", StepID: id, ToolCallID: "c1",
		Args: json.RawMessage(`{"command":"go test"}`), ResultContent: long,
	})
	tree := Build(SnapshotOf(st, false, at(5)))
	toolNode, parent := findKind(tree, KindTool)
	if toolNode == nil || parent == nil {
		t.Fatalf("the built tree has no tool row under a beat")
	}

	res, found := StackNodeOf(st, tree, toolNode.ID.Key)
	if !found {
		t.Fatalf("StackNodeOf(%q) not found", toolNode.ID.Key)
	}
	if res.Node.Parent != parent.ID.Key {
		t.Fatalf("node parent = %q, want the beat %q", res.Node.Parent, parent.ID.Key)
	}
	if len(res.Detail.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(res.Detail.Calls))
	}
	if got := res.Detail.Calls[0].Output; got != long {
		t.Fatalf("output = %d bytes, want the whole %d-byte output", len(got), len(long))
	}
	if want := "{\n  \"command\": \"go test\"\n}"; res.Detail.Calls[0].Args != want {
		t.Fatalf("args = %q, want %q", res.Detail.Calls[0].Args, want)
	}
}

func TestStackNodeOfMissesAnUnknownNode(t *testing.T) {
	st := projTestState(t)
	tree := Build(SnapshotOf(st, false, at(5)))
	if _, found := StackNodeOf(st, tree, "nope"); found {
		t.Fatal(`StackNodeOf("nope") was found, want not found`)
	}
}

func TestCallDetailCarriesItsHooksAsTheirJSON(t *testing.T) {
	hooks := []registry.HookMetadata{
		{Event: "PreToolUse", Decision: "allow"},
		{Event: "PostToolUse", FailedOpen: true},
	}
	want, err := json.Marshal(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if got := callDetail(registry.AuditEvent{ToolName: "shell.run", Hooks: hooks}).Hooks; string(got) != string(want) {
		t.Fatalf("hooks = %s, want %s", got, want)
	}
	if got := callDetail(registry.AuditEvent{ToolName: "shell.run"}).Hooks; len(got) != 0 {
		t.Fatalf("a call with no hooks carries %q, want none", got)
	}
}
