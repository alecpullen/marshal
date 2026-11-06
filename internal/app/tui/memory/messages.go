package memory

import "marshal/internal/acpwire"

// ShowMsg is emitted when the user selects a memory entry in the docked
// browser. It carries the entry the browser listed, so the parent model
// formats exactly what the user picked instead of re-reading it.
type ShowMsg struct{ Entry acpwire.MemoryEntry }

// DeletedMsg is emitted after a confirmed delete gesture. Title carries the
// memory text for the receipt; Err is non-nil when persistence failed.
type DeletedMsg struct {
	Title string
	Err   error
}

// ClosedMsg is emitted when the user dismisses the browser with Esc.
type ClosedMsg struct{}
