package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/sddauthor"
)

// planAuthorFinishedMsg carries the result of one authoring run back to the
// model. err is a hard construction/model/filesystem error; inspection
// diagnostics live inside result.Inspection.
type planAuthorFinishedMsg struct {
	result sddauthor.Result
	err    error
}

// runPlanAuthorCmd wraps an authoring run into a Bubble Tea command. It
// registers session work via BeginWork on construction and releases it via
// EndWork when the command executes, mirroring runAgentCmd. Callers must
// handle ErrSessionQuiescing from BeginWork before creating the command.
func runPlanAuthorCmd(ctx context.Context, state *session.State, author *sddauthor.Runner, req sddauthor.Request) tea.Cmd {
	return func() tea.Msg {
		defer state.EndWork()
		result, err := author.Run(ctx, req)
		return planAuthorFinishedMsg{result: result, err: err}
	}
}
