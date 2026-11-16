package settings

import (
	"errors"

	"marshal/internal/acpwire"
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

// OpenModelOptionsForProviderMsg is emitted when the settings browser wants
// to open the model-options editor for a specific provider's model pairs
// (the "Model options" action in the provider drill-in).
type OpenModelOptionsForProviderMsg struct {
	ProviderName string
}

type actionResultMsg struct {
	FieldID string
	Label   string
}

// marshalAccessMsg is one answer from the Studio access surface: a read, a
// level write or an instance selection. A stale write arrives here with Stale
// set and the value that won, so the row shows the conflict rather than an
// error the user cannot act on.
type marshalAccessMsg struct {
	view    acpwire.MarshalAccessView
	err     error
	fieldID string // the row the receipt belongs to, "" for a plain read
}

// errNoSession is what the Studio section reports when the browser holds no
// session to ask: a settings surface with no client seam behind it.
var errNoSession = errors.New("no session to read the Studio connection from")

// errAccessNotRead is what a Studio write reports before the first read has
// answered: a write must name the version it was edited from, and guessing one
// would let it overwrite a value nobody saw.
var errAccessNotRead = errors.New("the Studio connection has not been read yet")
