package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
)

// errUnknownDocAction is what a doc the TUI built reports for an ID it no
// longer holds: every entry it mints lives only as long as the panel that
// minted it.
var errUnknownDocAction = errors.New("that action is not available")

// localDocActions returns the act for a doc the TUI builds: each ID runs its
// entry in acts off the Update goroutine. The message carries the same act, so
// a doc an action opens keeps working.
func localDocActions(acts map[string]func() acpwire.CommandResult) func(actionID string) tea.Cmd {
	return func(actionID string) tea.Cmd {
		return func() tea.Msg {
			run, ok := acts[actionID]
			if !ok {
				return docActionMsg{err: errUnknownDocAction, act: localDocActions(acts)}
			}
			return docActionMsg{result: run(), act: localDocActions(acts)}
		}
	}
}

// sessionDocActions returns the act for a doc a session returned: each ID runs
// through sess.CommandAction under ctx, agent side. A doc the result carries
// keeps acting the same way.
func sessionDocActions(ctx context.Context, sess acpclient.Session) func(actionID string) tea.Cmd {
	return func(actionID string) tea.Cmd {
		return func() tea.Msg {
			res, err := sess.CommandAction(ctx, actionID)
			return docActionMsg{result: res, err: err, act: sessionDocActions(ctx, sess)}
		}
	}
}
