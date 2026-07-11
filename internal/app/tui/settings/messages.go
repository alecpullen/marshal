package settings

import (
	"marshal/internal/app/config"
)

type SavedMsg struct {
	Cfg config.Config
}

type CancelledMsg struct{}

type probeResultMsg struct {
	FieldID  string
	Provider string
	Models   []string
	Err      error
}

type actionResultMsg struct {
	FieldID string
	Label   string
}
