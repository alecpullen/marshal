package contract

// The terminal: a shell in the agent's workspace, streamed to the client.
//
// POST /api/agents/{id}/terminal opens one and answers with a
// TerminalOpened. GET …/terminal/{tid}/events is an SSE stream whose every
// message is a TerminalChunk. POST …/input, …/resize and …/release take a
// TerminalInput, a TerminalResize and no body; DELETE …/{tid} closes it.
//
// Typing holds the agent: it is paused from the first keystroke until the
// operator hands back or two minutes pass without input. Open has at most
// two terminals per agent.

// TerminalOpened is POST …/terminal's answer.
type TerminalOpened struct {
	TerminalID string `json:"terminalId"`
}

// TerminalChunk is one message of the terminal's event stream: output bytes
// in Data, base64 as SSE carries them, or the shell's end with its exit code
// and, when the shell never really started, why. A chunk carries one or the
// other, and the bridge replays retained output from the start, so a client
// never resumes a stream it lost.
type TerminalChunk struct {
	Data string `json:"data,omitempty"`
	Exit *int   `json:"exit,omitempty"`
	// Error is why the shell failed to start, on the chunk that ends the
	// stream.
	Error string `json:"error,omitempty"`
}

// TerminalInput is POST …/input's body: base64 of at most 64 KiB of
// keystrokes. A client sends in pieces, in order, one request at a time.
type TerminalInput struct {
	Data string `json:"data"`
}

// TerminalResize is POST …/resize's body, and what opening a terminal asks
// for: the shell's size in character cells.
type TerminalResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}
