package tui

import (
	"marshal/internal/app/tui/sessionsheet"
	"marshal/internal/viewmodel"
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

// changedMsg is the tea.Msg the change pump emits when the session signals
// that its status may have changed. ch is the channel the pump read, so a
// message from a replaced session's pump is recognised and dropped.
type changedMsg struct{ ch <-chan struct{} }

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
