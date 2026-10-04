package viewmodel

import (
	"fmt"
	"sort"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// Relation links a failing call (or the step holding it) to the steps that
// likely caused or fixed the failure.
type Relation struct{ CausedBy, FixedBy []NodeID }

// RelationIndex maps tool row IDs and step IDs to their relations.
type RelationIndex map[NodeID]Relation

func stepNodeID(step int64) NodeID {
	return NodeID{KindStep, fmt.Sprintf("step:%d", step)}
}

func isEdit(ev registry.AuditEvent) bool {
	return len(ev.FilesChanged) > 0 || isDiffTool(ev.ToolName)
}

// Relations derives "likely fixed by" and "likely caused by" links from a
// transcript. A failed shell-family call is fixed by the edits between it and
// the next passing run of the same command, and caused by the edits between
// the previous passing run and it. Edits without a step ID are skipped: their
// heuristic step IDs are not stable.
func Relations(items []session.TranscriptItem) RelationIndex {
	var audits []registry.AuditEvent
	for _, it := range items {
		if it.Audit != nil {
			audits = append(audits, *it.Audit)
		}
	}
	sort.SliceStable(audits, func(i, j int) bool { return audits[i].Timestamp.Before(audits[j].Timestamp) })

	byCommand := map[string][]int{}
	var order []string
	for i, ev := range audits {
		if !IsShellFamily(ev.ToolName) {
			continue
		}
		cmd := ToolTarget(ev)
		if _, ok := byCommand[cmd]; !ok {
			order = append(order, cmd)
		}
		byCommand[cmd] = append(byCommand[cmd], i)
	}

	editSteps := func(from, to int) []NodeID {
		var out []NodeID
		seen := map[NodeID]bool{}
		for i := from + 1; i < to; i++ {
			ev := audits[i]
			if !isEdit(ev) || ev.StepID == 0 {
				continue
			}
			if id := stepNodeID(ev.StepID); !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		return out
	}

	idx := RelationIndex{}
	for _, cmd := range order {
		calls := byCommand[cmd]
		for k, i := range calls {
			if !EventFailed(audits[i]) {
				continue
			}
			var rel Relation
			for _, j := range calls[k+1:] {
				if !EventFailed(audits[j]) {
					rel.FixedBy = editSteps(i, j)
					break
				}
			}
			for p := k - 1; p >= 0; p-- {
				if !EventFailed(audits[calls[p]]) {
					rel.CausedBy = editSteps(calls[p], i)
					break
				}
			}
			if len(rel.FixedBy) == 0 && len(rel.CausedBy) == 0 {
				continue
			}
			idx[ToolID(audits[i])] = rel
			if sid := audits[i].StepID; sid != 0 {
				sn := stepNodeID(sid)
				cur := idx[sn]
				cur.FixedBy = unionIDs(cur.FixedBy, rel.FixedBy)
				cur.CausedBy = unionIDs(cur.CausedBy, rel.CausedBy)
				idx[sn] = cur
			}
		}
	}
	return idx
}

func unionIDs(a, b []NodeID) []NodeID {
	seen := make(map[NodeID]bool, len(a))
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			seen[id] = true
			a = append(a, id)
		}
	}
	return a
}
