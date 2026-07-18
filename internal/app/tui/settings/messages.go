package settings

import (
	"marshal/internal/app/config"
)

// ChangedMsg reports a settings persistence attempt. Cfg is applied in memory
// even when SaveErr is non-nil; receipts are emitted only after a successful
// save.
type ChangedMsg struct {
	Receipts []string
	Cfg      config.Config
	SaveErr  error
}

// BrowserClosedMsg is emitted when the browser closes at its root.
type BrowserClosedMsg struct{}

type actionResultMsg struct {
	FieldID string
	Label   string
}
