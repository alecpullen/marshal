package settings

import (
	"marshal/internal/app/config"
)

// ChangedMsg reports a settings persistence attempt. Cfg is applied in memory
// even when SaveErr is non-nil or persistence is temporarily blocked; receipts
// are emitted only after a successful save. BlockedReason is set when a write
// was blocked by runtime work or a pending decision, separate from a save error.
type ChangedMsg struct {
	Receipts      []string
	Cfg           config.Config
	SaveErr       error
	BlockedReason string
}

// BrowserClosedMsg is emitted when the browser closes at its root.
type BrowserClosedMsg struct{}

// OpenConnectMsg is emitted when the settings browser wants to open the
// connect wizard (pressing a in the Providers collection or picking
// "Add a provider…" in a provider picker).
type OpenConnectMsg struct{}

type actionResultMsg struct {
	FieldID string
	Label   string
}
