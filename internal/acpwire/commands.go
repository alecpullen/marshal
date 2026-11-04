package acpwire

// The kinds session/command_list reports. A headless command runs through
// session/command, a TUI-only one is interactive and lives in the client, and a
// prompt command is run by sending its body as a prompt.
const (
	CommandKindHeadless = "headless"
	CommandKindTUIOnly  = "tui_only"
	CommandKindPrompt   = "prompt"
)

// CommandInfo is one entry in the session/command_list result. Name to Kind
// are what ACP reports today. Hidden, TUIOnly, HasHandler and PromptBody are
// what a client's dispatch reads (plan 6); ACP leaves them unset until P3.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Args        string `json:"args,omitempty"`
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind"`
	Hidden      bool   `json:"hidden,omitempty"`
	TUIOnly     bool   `json:"tuiOnly,omitempty"`
	HasHandler  bool   `json:"hasHandler,omitempty"`
	PromptBody  string `json:"promptBody,omitempty"`
}

// CommandListResult is the JSON-RPC result for session/command_list.
type CommandListResult struct {
	Commands []CommandInfo `json:"commands"`
}

// CommandResult is a command's output: plain Text for the transcript, or a Doc
// the client renders as a panel. AgentGoal, when set, is a user turn the client
// submits after rendering Text. ACP's session/command leaves it unset until P3.
type CommandResult struct {
	Text      string   `json:"text,omitempty"`
	Doc       *WireDoc `json:"doc,omitempty"`
	AgentGoal string   `json:"agentGoal,omitempty"`
}

// WireDoc is a command's structured panel.
type WireDoc struct {
	Title     string    `json:"title,omitempty"`
	FullFrame bool      `json:"fullFrame,omitempty"`
	Footer    string    `json:"footer,omitempty"`
	Rows      []WireRow `json:"rows,omitempty"`
}

// WireRow is one entry in a WireDoc. Exactly one shape applies: a section
// header (Header set), an action (ActionID set: Enter runs it through
// CommandAction, and ActionLabel is the right cell), a drill-in (Children
// set), or a read-only title and detail. A row whose action has no ID, as in
// ACP's session/command until P3, shows ActionLabel as plain text.
type WireRow struct {
	Header      string    `json:"header,omitempty"`
	Text        string    `json:"text,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Desc        string    `json:"desc,omitempty"`
	ActionLabel string    `json:"actionLabel,omitempty"`
	ActionID    string    `json:"actionId,omitempty"`
	Children    []WireRow `json:"children,omitempty"`
}
