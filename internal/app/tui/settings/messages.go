package settings

import (
	"marshal/internal/app/config"
)

// ChangedMsg reports settings changes that were applied and saved.
// Receipts are preformatted old-to-new lines for the transcript.
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
