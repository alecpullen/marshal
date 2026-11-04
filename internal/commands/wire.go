package commands

import (
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// Kind is the kind session/command_list reports for c: TUI-only first, then
// prompt, else headless.
func (c Command) Kind() string {
	switch {
	case c.TUIOnly:
		return acpwire.CommandKindTUIOnly
	case c.PromptBody != "":
		return acpwire.CommandKindPrompt
	default:
		return acpwire.CommandKindHeadless
	}
}

// Info is c's command_list entry with every field a client's dispatch reads.
func Info(c Command) acpwire.CommandInfo {
	return acpwire.CommandInfo{
		Name:        c.Name,
		Description: c.Description,
		Args:        c.Args,
		Group:       c.Group,
		Kind:        c.Kind(),
		Hidden:      c.Hidden,
		TUIOnly:     c.TUIOnly,
		HasHandler:  c.Handler != nil,
		PromptBody:  c.PromptBody,
	}
}

// WireResultOf converts res for the wire. mint, when non-nil, is called once
// for each row action, depth first in row order, and returns the ID the row
// carries. With a nil mint no row carries an ID.
func WireResultOf(res Result, mint func(action func(*session.State) Result) string) acpwire.CommandResult {
	return acpwire.CommandResult{
		Text:      res.Text,
		Doc:       WireDocOf(res.Doc, mint),
		AgentGoal: res.AgentGoal,
	}
}

// WireDocOf converts doc as WireResultOf does. It returns nil for a nil doc.
func WireDocOf(doc *Doc, mint func(action func(*session.State) Result) string) *acpwire.WireDoc {
	if doc == nil {
		return nil
	}
	return &acpwire.WireDoc{
		Title:     doc.Title,
		FullFrame: doc.FullFrame,
		Footer:    doc.Footer,
		Rows:      wireRowsOf(doc.Rows, mint),
	}
}

// wireRowsOf converts rows in order, minting a row's own action before it
// walks into the row's children.
func wireRowsOf(rows []Row, mint func(action func(*session.State) Result) string) []acpwire.WireRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]acpwire.WireRow, len(rows))
	for i, r := range rows {
		out[i] = acpwire.WireRow{
			Header:      r.Header,
			Text:        r.Text,
			Detail:      r.Detail,
			Desc:        r.Desc,
			ActionLabel: r.ActionLabel,
		}
		if r.Action != nil && mint != nil {
			out[i].ActionID = mint(r.Action)
		}
		out[i].Children = wireRowsOf(r.Children, mint)
	}
	return out
}
