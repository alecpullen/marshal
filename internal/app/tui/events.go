package tui

import "marshal/internal/app/session"

// jobCountMsg is the tea.Msg the job broker pump emits when a JobEvent
// arrives. Handling it sets the model's cached job count so the status
// line renders without polling session.State.
type jobCountMsg struct {
	count int
}

// steeringMsg is the tea.Msg the steering broker pump emits when a
// SteeringEvent lands. Handling it updates the cached queued
// count so the status line shows "queued <n>" and re-arms the pump.
type steeringMsg struct {
	queueLen int
	message  string
}

// workspaceMsg is the tea.Msg the workspace broker pump emits when the
// session's active root changes. Handling it re-reads git info for the new
// root so the status line's branch and wt: segments follow immediately,
// then re-arms the pump.
type workspaceMsg struct {
	activeRoot string
}

// subagentMsg is the tea.Msg the subagent broker pump emits when a
// subagent is registered or changes status. Handling it refreshes the
// transcript viewport so card state re-renders without polling.
type subagentMsg struct {
	view session.SubagentView
}

// railBaseRefMsg carries a freshly-read HEAD SHA for the changed-files rail.
// Emitted by railBaseRefCmd so the git subprocess stays off the UI thread.
// dir is the workspace active root the SHA was read from; the handler drops
// msgs whose dir is no longer the active root, so a stale in-flight cmd from
// a previous workspace/session cannot set the base ref from the wrong tree.
type railBaseRefMsg struct {
	dir string
	ref string
}
