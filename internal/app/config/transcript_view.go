package config

import "fmt"

// TranscriptView selects the conversation presentation used by the TUI.
type TranscriptView string

const (
	TranscriptLegacy   TranscriptView = "legacy"
	TranscriptNotebook TranscriptView = "notebook"
)

func (v TranscriptView) Valid() bool { return v == TranscriptLegacy || v == TranscriptNotebook }

// Effective returns the configured view, using legacy for empty or invalid
// values so hand-edited files never prevent the TUI from starting.
func (v TranscriptView) Effective() TranscriptView {
	if !v.Valid() {
		return TranscriptLegacy
	}
	return v
}

func ParseTranscriptView(value string) (TranscriptView, error) {
	v := TranscriptView(value)
	if !v.Valid() {
		return "", fmt.Errorf("invalid transcript view %q (accepted: \"legacy\", \"notebook\")", value)
	}
	return v, nil
}
