package tui

import (
	"marshal/internal/app/session"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/tools/native"
	"marshal/internal/watch"
)

// jobCountMsg is the tea.Msg the job broker pump emits when a JobEvent
// arrives. Handling it sets the model's cached job count so the status
// line renders without polling session.State. jobs carries the JobInfo
// snapshot so the job lane can render per-job detail.
type jobCountMsg struct {
	count int
	jobs  []native.JobInfo
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

// watchMsg is the tea.Msg the watch broker pump emits when a watch.Event
// arrives. Handling it updates the model's cached watch snapshot so the
// activity lane can render watch rows without polling.
type watchMsg struct {
	event watch.Event
}

// sheetBaseRefMsg carries a freshly-read HEAD SHA and the changed files against it.
// Emitted by sheetBaseRefCmd so the git subprocess stays off the UI thread.
// dir is the workspace active root the SHA was read from; the handler drops
// msgs whose dir is no longer the active root, so a stale in-flight cmd from
// a previous workspace/session cannot set the base ref from the wrong tree.
type sheetBaseRefMsg struct {
	dir string
	ref string
	// changed is the working-tree diff against ref, read in the same
	// off-thread command so the handler does no git work of its own.
	changed []sessionsheet.ChangedFile
}
