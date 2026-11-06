package acpwire

// These are what the browser surfaces read: the resume picker's session list
// and the project's memories. Times are Unix milliseconds (see Millis).

// SessionEntry is one stored session the resume picker lists (C11, plan 8b).
// UpdatedAt is Unix milliseconds.
type SessionEntry struct {
	SessionID    string `json:"sessionId"`
	Title        string `json:"title,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
	MessageCount int    `json:"messageCount,omitempty"`
}

// MemoryEntry is one of the project's memories. CreatedAt is Unix
// milliseconds.
type MemoryEntry struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Content    string `json:"content"`
	Confidence string `json:"confidence,omitempty"`
	Scope      string `json:"scope,omitempty"`
	CreatedAt  int64  `json:"createdAt,omitempty"`
}
