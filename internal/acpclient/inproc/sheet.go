package inproc

import (
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/viewmodel"
)

// Sheet is what the session sheet's sections read (C5): the session's own
// sheet when subagentID is 0, or the sheet of the subagent with that ID
// anywhere in the session's tree. An ID that names no child session reads as
// 0, and the result's SubagentID says which sheet was read.
//
// The database and git values stay empty until RefreshSheet fills them.
func (s *Session) Sheet(subagentID int64) acpwire.SessionSheet {
	st := s.state
	id := int64(0)
	if subagentID != 0 {
		if child := childState(s.state, subagentID); child != nil {
			st = child
			id = subagentID
		}
	}
	sheet := activityOf(st)
	sheet.SubagentID = id
	if id == 0 {
		sheet.Context = contextOf(st.ContextPack())
	}
	return sheet
}

// childState is the state of the subagent with id anywhere in st's tree, or
// nil when no view names it. A view with no child state is skipped: it has no
// activity of its own to read.
func childState(st *session.State, id int64) *session.State {
	for _, v := range st.Subagents() {
		if v.Child == nil {
			continue
		}
		if v.ID == id {
			return v.Child
		}
		if found := childState(v.Child, id); found != nil {
			return found
		}
	}
	return nil
}

// activityOf fills the sheet values a state carries in memory. The database
// and git values are left for RefreshSheet.
func activityOf(st *session.State) acpwire.SessionSheet {
	events := st.AuditLog()
	budget := st.ToolBudget()
	sandbox := st.SandboxInfo()
	return acpwire.SessionSheet{
		StartedAt: acpwire.Millis(st.StartedAt),
		Tools:     viewmodel.SheetToolsOf(events),
		Files:     viewmodel.SheetFilesOf(events),
		Rules:     nilIfEmpty(st.SessionRules()),
		Skills:    nilIfEmpty(st.ActiveSkills()),
		ToolBudget: acpwire.SheetToolBudget{
			Used: budget.Used,
			Max:  budget.Max,
		},
		LoadedTools: len(st.LoadedToolNames()),
		Sandbox: acpwire.SheetSandbox{
			Backend:      sandbox.Backend,
			AllowNetwork: st.Config.Tools.Shell.AllowNetwork,
		},
	}
}

// contextOf copies a context pack's size and its sections' sizes. Every
// section is listed, an empty one included: the context section is relevant
// whenever the pack has any section. A pack with no sections gives nil
// Sections.
func contextOf(p contextpack.Pack) acpwire.SheetContext {
	out := acpwire.SheetContext{
		MaxTokens:       p.TokenUsage.MaxTokens,
		EstimatedTokens: p.TokenUsage.EstimatedTokens,
	}
	if len(p.Sections) == 0 {
		return out
	}
	out.Sections = make([]acpwire.SheetContextSection, 0, len(p.Sections))
	for _, s := range p.Sections {
		out.Sections = append(out.Sections, acpwire.SheetContextSection{
			Title:           s.Title,
			EstimatedTokens: s.EstimatedTokens,
		})
	}
	return out
}

// nilIfEmpty returns nil for an empty slice, so the wire value encodes as
// absent under omitempty.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
