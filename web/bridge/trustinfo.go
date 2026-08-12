package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func projectTrust(root string) string {
	if _, err := os.Stat(filepath.Join(root, ".marshal", "config.toml")); err != nil {
		return "na"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "untrusted"
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "share", "marshal", "trust.json"))
	if err != nil {
		return "untrusted"
	}
	var records map[string]struct {
		Trusted bool `json:"trusted"`
	}
	if json.Unmarshal(data, &records) != nil {
		return "untrusted"
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolved = root
	}
	if rec, ok := records[resolved]; ok && rec.Trusted {
		return "trusted"
	}
	if rec, ok := records[root]; ok && rec.Trusted {
		return "trusted"
	}
	return "untrusted"
}
