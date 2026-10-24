package presetsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProvenanceVersion is the sidecar schema version.
const ProvenanceVersion = 1

// Provenance records which preset fields models.dev last applied, so a
// reconnect can refresh them without overwriting user edits.
type Provenance struct {
	Version int                         `json:"version"`
	Presets map[string]PresetProvenance `json:"presets"`
}

// PresetProvenance is one preset's catalog binding and managed fields.
type PresetProvenance struct {
	CatalogProvider string                `json:"catalog_provider"`
	CatalogModel    string                `json:"catalog_model"`
	Fields          map[string]FieldState `json:"fields"`
}

// FieldState is the last value Marshal applied to one field.
type FieldState struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

// ProvenancePath returns the sidecar path under dataDir.
func ProvenancePath(dataDir string) string {
	return filepath.Join(dataDir, "models-dev", "preset-provenance.json")
}

// LoadProvenance reads the sidecar. A missing file is an empty provenance; a
// corrupt file is an error so callers can fail safe.
func LoadProvenance(dataDir string) (*Provenance, error) {
	data, err := os.ReadFile(ProvenancePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return &Provenance{Version: ProvenanceVersion, Presets: map[string]PresetProvenance{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode preset provenance: %w", err)
	}
	if p.Presets == nil {
		p.Presets = map[string]PresetProvenance{}
	}
	return &p, nil
}

// SaveProvenance writes the sidecar atomically.
func SaveProvenance(dataDir string, p *Provenance) error {
	if p == nil {
		return nil
	}
	path := ProvenancePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
