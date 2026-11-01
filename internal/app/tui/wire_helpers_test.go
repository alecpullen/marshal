package tui

import (
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/viewmodel"
)

// wireOf returns n's wire node and its children's, in order, projected
// against the active root workdir.
func wireOf(n *viewmodel.Node, workdir string) (*acpwire.WireNode, []*acpwire.WireNode) {
	idx := wireIndex(viewmodel.Project([]*viewmodel.Node{n}, workdir))
	w := idx[n.ID.Key]
	rows := make([]*acpwire.WireNode, 0, len(w.Children))
	for _, key := range w.Children {
		rows = append(rows, idx[key])
	}
	return w, rows
}

// wireTool is the wire form of a call row holding evs.
func wireTool(workdir string, evs ...registry.AuditEvent) *acpwire.WireTool {
	n := &viewmodel.Node{ID: viewmodel.ToolID(evs[0]), Kind: viewmodel.KindTool, Tools: evs}
	w, _ := wireOf(n, workdir)
	return w.Tool
}

// wireRunning is the wire form of a running call's row.
func wireRunning(a session.ActiveToolCall) *acpwire.WireTool {
	n := &viewmodel.Node{ID: viewmodel.ActiveToolID(a), Kind: viewmodel.KindTool, Active: &a}
	w, _ := wireOf(n, "")
	return w.Tool
}

// wirePlan is the wire form of a beat's plan line.
func wirePlan(p viewmodel.PlanUpdate) *acpwire.WirePlan {
	n := &viewmodel.Node{
		ID:         viewmodel.NodeID{Kind: viewmodel.KindPlanUpdate, Key: "plan"},
		Kind:       viewmodel.KindPlanUpdate,
		PlanUpdate: &p,
	}
	w, _ := wireOf(n, "")
	return w.Plan
}
