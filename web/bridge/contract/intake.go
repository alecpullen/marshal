package contract

import "time"

// PendingSubmission is one submission awaiting an operator's decision, as
// GET /api/pending lists it.
type PendingSubmission struct {
	Auto *bool `json:"auto"`
	// ClientID is the submitting client's id.
	ClientID string `json:"clientId"`
	// CreatedAt is when the submission was stored.
	CreatedAt time.Time `json:"createdAt"`
	// ExpiresAt is when it stops being answerable.
	ExpiresAt time.Time `json:"expiresAt"`
	ID        string    `json:"id"`
	Mode      string    `json:"mode"`
	Origin    string    `json:"origin"`
	// OwnerID is never set by today's bridge; unset is the local owner.
	OwnerID string `json:"ownerId,omitempty"`
	Plan    string `json:"plan"`
	Prompt  string `json:"prompt"`
	Ref     string `json:"ref"`
	RepoID  string `json:"repoId"`
	// Title is what the operator reads when deciding. It comes from the
	// calling agent and is therefore untrusted: render as text, never as
	// markup.
	Title string `json:"title"`
}

// ApproveResult is an approve answer's {"agentId": …, "status": …}.
type ApproveResult struct {
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
}

// ActionStatus is the discard and deny answers' {"status": …}.
type ActionStatus struct {
	Status string `json:"status"`
}
