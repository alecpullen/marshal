package tui

import (
	"marshal/internal/acpwire"
)

// workMsg is the tea.Msg the Work snapshot pump emits when a WorkSnapshot
// event arrives. subscriptionID names the subscription the pump captured, so
// an event from a superseded subscription (a prior conversation, or a rebind
// that replaced the channel) is ignored rather than applied.
type workMsg struct {
	snapshot       acpwire.WorkSnapshot
	subscriptionID uint64
}

// workRequestMsg is contract C5's UI→pump request. Every Tasks-panel Work
// action emits one for the model's pump to service; the panel itself never
// calls the Work service.
type workRequestMsg struct {
	conversationID, panelID, requestID string
	kind, taskKey, resultRevision      string
	query                              acpwire.WorkQuery
	raw                                bool
}

// workPageMsg is the reply to one list request. It carries the captured
// conversation, panel and request IDs so the result can be validated against
// the panel and request that are still current before it is applied.
type workPageMsg struct {
	conversationID, panelID, requestID string
	query                              acpwire.WorkQuery
	page                               acpwire.WorkPage
	err                                error
}

// workDetailMsg is the reply to one inspect request, carrying the same
// captured identity so a late detail cannot open the wrong item.
type workDetailMsg struct {
	conversationID, panelID, requestID string
	detail                             acpwire.WorkDetail
	err                                error
}

// workActionMsg is the reply to a stop or acknowledgement request. stop
// describes the accepted request only; source snapshots remain the authority
// on the item's actual state.
type workActionMsg struct {
	conversationID, taskKey, resultRevision, requestID string
	kind                                               string
	stop                                               acpwire.WorkStopResult
	err                                                error
}

// changedMsg is the tea.Msg the change pump emits when the session signals
// that its status may have changed. ch is the channel the pump read, so a
// message from a replaced session's pump is recognised and dropped.
type changedMsg struct{ ch <-chan struct{} }
