package acpwire

// MessageQuery selects the transcript messages Session.Messages returns (C11,
// plan 8a). The zero value matches every message.
type MessageQuery struct {
	// Role keeps only messages of one role, RoleUser or RoleAssistant. Empty
	// matches every role.
	Role string `json:"role,omitempty"`
	// Final keeps only final answers whose text is not blank.
	Final bool `json:"final,omitempty"`
	// Last keeps only the newest Last matches. Zero keeps them all.
	Last int `json:"last,omitempty"`
}

// MessagePage is what Session.Messages returns: the kept messages, oldest
// first, and how many messages matched before Last cut them.
type MessagePage struct {
	Messages []HistoryMessage `json:"messages,omitempty"`
	Total    int              `json:"total,omitempty"`
}

// HistoryMessage is one transcript message as a query returns it.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Final   bool   `json:"final,omitempty"`
}
