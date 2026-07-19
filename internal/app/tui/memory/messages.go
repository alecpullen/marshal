package memory

// ShowMsg is emitted when the user selects a memory entry in the docked
// browser. The parent model prints the entry detail to the transcript.
type ShowMsg struct{ ID int64 }

// DeletedMsg is emitted after a confirmed delete gesture. Title carries the
// memory text for the receipt; Err is non-nil when persistence failed.
type DeletedMsg struct {
	Title string
	Err   error
}

// ClosedMsg is emitted when the user dismisses the browser with Esc.
type ClosedMsg struct{}
