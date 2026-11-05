package acpwire

import "encoding/json"

// PresetSetup is what preset setup proposes after a connect, apart from the
// presets themselves (C10, plan 7b).
type PresetSetup struct {
	// Provenance is the updated models.dev provenance sidecar. It is opaque to
	// the client, which hands it back to SaveProvenance unchanged. It is nil
	// when the sidecar could not be read; then nothing may be saved.
	Provenance json.RawMessage `json:"provenance,omitempty"`
	// Managed names the presets the provenance manages, sorted. It is empty
	// when Provenance is nil.
	Managed  []string `json:"managed,omitempty"`
	Prepared int      `json:"prepared,omitempty"`
	Matched  int      `json:"matched,omitempty"`
	Fallback int      `json:"fallback,omitempty"`
}
