package tui

import (
	"marshal/internal/app/session"
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// workMsg is the tea.Msg the Work snapshot pump emits when a WorkSnapshot
// event arrives. subscriptionID names the subscription the pump captured, so
// an event from a superseded subscription (a prior conversation, or a rebind
// that replaced the channel) is ignored rather than applied.
type workMsg struct {
	snapshot       viewmodel.WorkSnapshot
	subscriptionID uint64
}

// workRequestMsg is contract C5's UI→pump request. Every Tasks-panel Work
// action emits one for the model's pump to service; the panel itself never
// calls the Work service.
type workRequestMsg struct {
	conversationID, panelID, requestID string
	kind, taskKey, resultRevision      string
	query                              viewmodel.WorkQuery
	raw                                bool
}

// workPageMsg is the reply to one list request. It carries the captured
// conversation, panel and request IDs so the result can be validated against
// the panel and request that are still current before it is applied.
type workPageMsg struct {
	conversationID, panelID, requestID string
	query                              viewmodel.WorkQuery
	page                               viewmodel.WorkPage
	err                                error
}

// workDetailMsg is the reply to one inspect request, carrying the same
// captured identity so a late detail cannot open the wrong item.
type workDetailMsg struct {
	conversationID, panelID, requestID string
	detail                             viewmodel.WorkDetail
	err                                error
}

// workActionMsg is the reply to a stop or acknowledgement request. stop
// describes the accepted request only; source snapshots remain the authority
// on the item's actual state.
type workActionMsg struct {
	conversationID, taskKey, resultRevision, requestID string
	kind                                               string
	stop                                               viewmodel.WorkStopResult
	err                                                error
}

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
//
// snapshot and receipt are the additive Send now channels: the same event
// carries the current steering snapshot and, when a request moved, that
// request's receipt. Either may be nil — an ordinary queue-only publish
// carries neither.
type steeringMsg struct {
	queueLen int
	message  string
	snapshot *session.SteeringSnapshot
	receipt  *session.SteeringReceipt
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
