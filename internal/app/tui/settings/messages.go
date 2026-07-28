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
	// Saved reports that the panel already persisted this change to its
	// target file; the model must reload but not save again. Global-target
	// commits set this so the project file is never touched.
	Saved bool

	// GlobalTarget indicates this change was written (or attempted) against
	// the user-global config file. When true and Saved is false, the model
	// must NOT fall through to persistAndReload (which writes the project
	// config), because writing a global value into the project config
	// violates the config-model provenance constraint.
	GlobalTarget bool
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
