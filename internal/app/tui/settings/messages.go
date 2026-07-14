package settings

import (
	"marshal/internal/app/config"
)

type SavedMsg struct {
	Cfg config.Config
}

type CancelledMsg struct{}

type actionResultMsg struct {
	FieldID string
	Label   string
}
