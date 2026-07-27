package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ManifestFileName is the optional plugin metadata file.
const ManifestFileName = "plugin.toml"

// Manifest is optional plugin metadata. Both fields are informational
// (shown in install confirmations and `plugin list`); all content is
// discovered by convention regardless of what the manifest declares.
type Manifest struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
}

// ParseManifest reads dir/plugin.toml. ok is false when the file is
// absent — not an error, phase-1 plugins have no manifest. A present but
// malformed file is an error: the author clearly intended metadata
// marshal cannot understand.
func ParseManifest(dir string) (m Manifest, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return Manifest{}, false, nil
		}
		return Manifest{}, false, fmt.Errorf("read %s: %w", ManifestFileName, err)
	}
	if err := toml.Unmarshal(raw, &m); err != nil {
		return Manifest{}, false, fmt.Errorf("parse %s: %w", ManifestFileName, err)
	}
	return m, true, nil
}
