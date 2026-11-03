package acpwire

// The intents an end names (C8): closing the conversation, or replacing it
// with a new or resumed one. A replacement never runs the exit postmortem.
const (
	EndIntentExit    = "exit"
	EndIntentReplace = "replace"
)

// EndWork is the work an end of the session would stop, oldest first: what
// the active-work warning shows.
type EndWork struct {
	Items []EndWorkItem `json:"items,omitempty"`
}

// EndWorkItem is one piece of work an end would stop. ID is opaque and
// distinct for every item. Group is the ID of the operation an attached
// command belongs to, empty when the item stands alone.
type EndWorkItem struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Group string `json:"group,omitempty"`
}

// EndAcceptParams is one acceptance of an end: its intent and the work IDs the
// user was shown. With RequireDisplayed, the end stops nothing while work the
// user was not shown is active.
type EndAcceptParams struct {
	Intent           string   `json:"intent"`
	DisplayedIDs     []string `json:"displayedIds,omitempty"`
	RequireDisplayed bool     `json:"requireDisplayed,omitempty"`
}

// EndDecision is an acceptance's outcome. Accepted false with Expanded set
// means nothing was frozen or stopped: Work is the membership to show and ask
// about again.
type EndDecision struct {
	Accepted bool     `json:"accepted,omitempty"`
	Expanded []string `json:"expanded,omitempty"`
	Work     EndWork  `json:"work"`
}
