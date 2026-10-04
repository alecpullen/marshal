package viewmodel

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func wireByID(t *testing.T, tree WireTree) map[string]WireNode {
	t.Helper()
	out := map[string]WireNode{}
	for _, n := range tree.Nodes {
		if _, dup := out[n.ID]; dup {
			t.Fatalf("duplicate wire id %q", n.ID)
		}
		out[n.ID] = n
	}
	return out
}

func TestProjectLinksParentsAndChildren(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{
			userMsg(1, 0),
			narration(2, 7, 1, "Reading the config loader. Then the tests."),
			audit("file.read", 7, "c1", 2),
			final(3, 5),
		},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
		Now:   at(6),
	}))
	if len(tree.Roots) != 1 || tree.Roots[0] != "turn:1" {
		t.Fatalf("roots = %v, want [turn:1]", tree.Roots)
	}
	byID := wireByID(t, tree)
	seen := map[string]bool{}
	for i, n := range tree.Nodes {
		if n.Parent != "" && !seen[n.Parent] {
			t.Fatalf("node %d (%s) listed before its parent %s", i, n.ID, n.Parent)
		}
		seen[n.ID] = true
		for _, c := range n.Children {
			if byID[c].Parent != n.ID {
				t.Fatalf("child %s of %s has parent %q", c, n.ID, byID[c].Parent)
			}
		}
	}
	st := byID["step:7"]
	if st.Kind != "step" || st.Step == nil {
		t.Fatalf("step:7 = %+v", st)
	}
	if st.Step.Headline != "Reading the config loader." || st.Step.Rest != "Then the tests." || st.Step.Inferred {
		t.Fatalf("headline = %q rest = %q inferred = %v", st.Step.Headline, st.Step.Rest, st.Step.Inferred)
	}
	tool := byID["tool:7:c1"]
	if tool.Kind != "tool" || tool.Tool == nil || tool.Tool.Display != "Read file" || len(tool.Tool.Calls) != 1 {
		t.Fatalf("tool:7:c1 = %+v", tool)
	}
	if fin := byID["msg:3"]; fin.Kind != "final" || fin.Message == nil || fin.Message.Content != "done" {
		t.Fatalf("msg:3 = %+v", fin)
	}
	if rc, ok := byID["receipt:turn:1"]; !ok || rc.Receipt == nil {
		t.Fatalf("finished turn has no receipt: %+v", rc)
	}
}

func TestProjectInfersHeadlineWithoutNarration(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), audit("file.read", 7, "c1", 2), audit("file.read", 7, "c2", 3)},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(4)}},
		Now:   at(6),
	}))
	st := wireByID(t, tree)["step:7"].Step
	if st == nil || st.Headline != "read 2 files" || !st.Inferred {
		t.Fatalf("step = %+v, want inferred \"read 2 files\"", st)
	}
}

func TestProjectRunningCallIsLive(t *testing.T) {
	tree := Project(Build(Snapshot{
		Items:       []session.TranscriptItem{userMsg(1, 0)},
		Steps:       []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1)}},
		ActiveTools: []session.ActiveToolCall{{Name: "shell.run", Args: "$ go test ./...", StartedAt: at(2), StepID: 7, ToolCallID: "c9"}},
		Busy:        true,
		Now:         at(3),
	}))
	n := wireByID(t, tree)["tool:7:c9"]
	if !n.Live || n.Tool == nil || n.Tool.Running == nil || n.Tool.Running.Target != "$ go test ./..." {
		t.Fatalf("running call = %+v", n)
	}
}

func TestProjectCapsLargeText(t *testing.T) {
	big := strings.Repeat("é", WireTextCap) // 2 bytes per rune
	ev := registry.AuditEvent{Timestamp: at(2), ToolName: "shell.run", StepID: 7, ToolCallID: "c1", ResultContent: big}
	tree := Project(Build(Snapshot{
		Items: []session.TranscriptItem{userMsg(1, 0), {Timestamp: at(2), Kind: session.KindAudit, Audit: &ev}},
		Steps: []session.Step{{ID: 7, TurnMsgID: 1, StartedAt: at(1), EndedAt: at(3)}},
		Now:   at(4),
	}))
	c := wireByID(t, tree)["tool:7:c1"].Tool.Calls[0]
	if !c.Truncated || len(c.Output) > WireTextCap || !strings.HasPrefix(big, c.Output) {
		t.Fatalf("output len %d truncated %v", len(c.Output), c.Truncated)
	}
	if _, err := json.Marshal(tree); err != nil {
		t.Fatal(err)
	}
}

func TestKindStringCoversEveryKind(t *testing.T) {
	for k := KindTurn; k <= KindReceipt; k++ {
		if s := k.String(); s == "" || s == "unknown" {
			t.Fatalf("Kind(%d) has no wire name", k)
		}
	}
}
