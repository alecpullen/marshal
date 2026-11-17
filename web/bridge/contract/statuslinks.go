package contract

// The status-link routes (web/bridge/status.go): a revocable public link to one
// agent's progress. The token is in the URL and is shown once; a listed link
// never carries it, nor its hash.

// StatusLink is one link as GET /api/status-links lists it.
type StatusLink struct {
	ID        string  `json:"id"`
	AgentID   string  `json:"agentId"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt string  `json:"expiresAt"`
	RevokedAt *string `json:"revokedAt,omitempty"`
}

// StatusLinkCreate is POST /api/status-links' body. A zero TTL takes the
// bridge's default.
type StatusLinkCreate struct {
	AgentID  string `json:"agentId"`
	TTLHours int    `json:"ttlHours,omitempty"`
}

// StatusLinkCreated is that route's answer: the only time the URL is ever
// given out.
type StatusLinkCreated struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}
