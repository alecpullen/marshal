package tui

import (
	tea "charm.land/bubbletea/v2"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/gitinfo"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

// pumpJobEvents reads from the model's persistent job subscription and returns a tea.Cmd that
// blocks until the next JobEvent arrives (or ctx is cancelled), then
// returns a jobCountMsg. The model calls this repeatedly from Update to
// keep the pump alive without creating a new broker subscription per event.
func pumpJobEvents(ch <-chan pubsub.Event[native.JobEvent]) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil // broker closed / ctx cancelled → pump stops
		}
		return jobCountMsg{count: ev.Payload.Count}
	}
}

// pumpSteeringEvents reads from the model's persistent steering subscription and returns a
// tea.Cmd that blocks until the next SteeringEvent lands, then returns
// a steeringMsg. Re-arming reads from the same channel so stale subscribers
// do not accumulate while the F16 queue is active.
func pumpSteeringEvents(ch <-chan pubsub.Event[session.SteeringEvent]) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return steeringMsg{queueLen: ev.Payload.QueueLen, message: ev.Payload.Message}
	}
}

// pumpWorkspaceEvents reads from the model's persistent workspace
// subscription and returns a tea.Cmd that blocks until the next
// WorkspaceEvent lands, then returns a workspaceMsg. Re-arming reads from
// the same channel, matching pumpSteeringEvents.
func pumpWorkspaceEvents(ch <-chan pubsub.Event[session.WorkspaceEvent]) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return workspaceMsg{activeRoot: ev.Payload.Workspace.ActiveRoot}
	}
}

// railBaseRefCmd shells out to git rev-parse off the UI thread and returns
// the result as a railBaseRefMsg. dir is the workspace active root.
func railBaseRefCmd(dir string) tea.Cmd {
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		return railBaseRefMsg{ref: gitinfo.HeadSHA(dir)}
	}
}
