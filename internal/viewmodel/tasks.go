package viewmodel

import (
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

const todoWriteTool = "todo.write"

// ReceiptInfo is the one-line summary closing a finished turn.
type ReceiptInfo struct {
	Duration time.Duration
	Tasks    int
	Steps    int
	Tools    int
	Files    int
	Usage    string
	Salvaged bool
}

// IsShellFamily reports whether a tool's subject is a command line.
func IsShellFamily(name string) bool { return name == "shell.run" || name == "test.run" }

// EventFailed is the one definition of a failed call: an error, a denial, or
// a non-zero exit. The task failure rule and the step glyph both use it.
func EventFailed(ev registry.AuditEvent) bool {
	return ev.Error != "" || ev.Approval == registry.ApprovalDenied ||
		(ev.CommandExitCode != nil && *ev.CommandExitCode != 0)
}

// effectiveTodoID is the task a step renders under, and whether the binding
// was re-bound. A step is bound to the task active when it started, so a step
// that narrates and then marks the next todo in progress with todo.write would
// sit under the previous task. When todo.write is its only call, it is
// re-bound to the todo the same actor's next step in the turn was bound to.
// That step's binding is stored, so the answer never changes once the next
// step exists; until then the beat is still live.
func effectiveTodoID(info *StepInfo, noOtherCalls bool, turnSteps []session.Step) (id string, rebound bool) {
	if len(info.TodoWrites) == 0 || !noOtherCalls || len(info.Narration) == 0 || info.Step.ID == 0 {
		return info.Step.TodoID, false
	}
	for _, st := range turnSteps {
		if st.ID == 0 || st.ID == info.Step.ID {
			continue
		}
		if st.Actor.Role != info.Step.Actor.Role || st.Actor.Label != info.Step.Actor.Label {
			continue
		}
		if !st.StartedAt.After(info.Step.StartedAt) {
			continue
		}
		if st.TodoID != "" {
			return st.TodoID, true
		}
		return info.Step.TodoID, false
	}
	return info.Step.TodoID, false
}

// receipt builds the closing summary for a finished turn, or nil when the
// turn has no final answer or no steps. before and after are the newest plan
// in effect before the turn and once it is included.
func receipt(turnKey string, nodes []*Node, userMsg *session.Message, before, after *PlanUpdate) *Node {
	var final *session.Message
	for _, n := range nodes {
		if n.Kind == KindFinal && n.Item != nil && n.Item.Message != nil {
			final = n.Item.Message
		}
	}
	if final == nil {
		return nil
	}
	r := &ReceiptInfo{Usage: final.Usage, Salvaged: final.Salvaged}
	if userMsg != nil && !final.CreatedAt.IsZero() && !userMsg.CreatedAt.IsZero() {
		r.Duration = final.CreatedAt.Sub(userMsg.CreatedAt)
	}
	files := map[string]bool{}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch {
			case n.Kind == KindStep && n.Step != nil:
				r.Steps++
			case n.Kind == KindTool:
				for _, ev := range n.Tools {
					r.Tools++
					for _, f := range ev.FilesChanged {
						files[f] = true
					}
				}
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	if r.Steps == 0 {
		return nil
	}
	r.Files = len(files)
	// The task count is the number of items the turn's plan lines moved to
	// completed, so a list rewritten later cannot change a settled receipt.
	if after != nil && after != before {
		beforeDone := 0
		if before != nil {
			beforeDone = before.Done()
		}
		r.Tasks = max(0, after.Done()-beforeDone)
	}
	n := &Node{ID: NodeID{KindReceipt, "receipt:" + turnKey}, Kind: KindReceipt, Receipt: r}
	b := newHash()
	b.f("rc|%d|%d|%d|%d|%d|%s|%v|", r.Duration, r.Tasks, r.Steps, r.Tools, r.Files, r.Usage, r.Salvaged)
	n.Version = b.sum()
	return n
}
