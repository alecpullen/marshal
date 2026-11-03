package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// planAuthorFinishedMsg reports an authoring run's end. refusal is the note a
// refused start writes.
type planAuthorFinishedMsg struct {
	result  acpwire.AuthoredPlan
	err     error
	refusal string
}

// planAuthorCmd runs one authoring run through the session and reports its
// end.
func planAuthorCmd(ctx context.Context, sess acpclient.Session, params acpwire.AuthorPlanParams) tea.Cmd {
	return func() tea.Msg {
		result, err := sess.AuthorPlan(ctx, params)
		return planAuthorFinishedMsg{result: result, err: err, refusal: "Cannot start work"}
	}
}
