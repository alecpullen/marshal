package contract

import "time"

// ReviewComment is one line comment on an agent's diff.
//
// The routes are GET and POST /api/agents/{id}/review/comments, and
// POST /api/agents/{id}/review/comments/{cid}/resolve. A comment is
// delivered to the agent as steering when a turn is running and as a new
// prompt when it is not, so the thread it starts is the agent's reply in the
// same conversation.
type ReviewComment struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	// Side is "old" or "new": which side of the diff the line is on.
	Side string `json:"side"`
	// Quote is the diff lines the comment is anchored to, shown to the
	// agent above the body.
	Quote     string    `json:"quote"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	// SentAt is when the bridge delivered the comment; it is zero until it
	// has.
	SentAt time.Time `json:"sentAt"`
	// ResolvedAt is when the operator resolved the thread, absent while it
	// is open.
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	OwnerID    string     `json:"ownerId"`
}

// ReviewCommentRequest is a comment's operator-supplied fields: the body of
// POST /api/agents/{id}/review/comments. Everything else about a comment is
// the bridge's to fill in.
type ReviewCommentRequest struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Side  string `json:"side"`
	Quote string `json:"quote"`
	Body  string `json:"body"`
}
