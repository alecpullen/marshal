package contract

import "time"

// Inbox is the fleet's inbox (P5 F2).
type Inbox struct {
	Groups InboxGroups `json:"groups"`
	Queue  []InboxItem `json:"queue"`
}

// InboxGroups are agent IDs, each list in urgency order.
type InboxGroups struct {
	NeedsYou []string `json:"needsYou"`
	Running  []string `json:"running"`
	Ready    []string `json:"ready"`
	Earlier  []string `json:"earlier"`
}

// InboxItemKind is what an entry in the needs-you queue is.
type InboxItemKind string

// The kinds of inbox item.
const (
	InboxItemAgent  InboxItemKind = "agent"
	InboxItemIntake InboxItemKind = "intake"
)

// InboxItem is one entry in the needs-you queue, oldest first.
type InboxItem struct {
	Kind       InboxItemKind      `json:"kind"`
	AgentID    string             `json:"agentId,omitempty"`
	Submission *PendingSubmission `json:"submission,omitempty"`
	At         time.Time          `json:"at"`
}

// InboxDelta is the "inbox" delta on the fleet stream.
type InboxDelta struct {
	Kind  DeltaKind `json:"kind"`
	Inbox Inbox     `json:"inbox"`
}
