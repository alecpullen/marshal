package contract

// Decision is a POST /api/permissions/{id} body.
type Decision struct {
	Approved   bool              `json:"approved"`
	Edited     string            `json:"edited,omitempty"`
	ModeAccess *ModeAccessChoice `json:"modeAccess,omitempty"`
}

// ModeAccessChoice is a typed capability choice, forwarded untouched.
type ModeAccessChoice struct {
	RequestID string      `json:"requestId"`
	SessionID string      `json:"sessionId"`
	Current   AccessState `json:"current"`
	Mode      string      `json:"mode,omitempty"`
}

// Answer is one question-and-response pair.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Answers is a POST /api/questions/{id} body.
type Answers struct {
	Answers  []Answer `json:"answers,omitempty"`
	Declined bool     `json:"declined,omitempty"`
}

// RecentPrompts is the GET /api/prompts/recent answer.
type RecentPrompts struct {
	Prompts []string `json:"prompts"`
}
