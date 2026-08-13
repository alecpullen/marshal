package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// projectTrust reports a project's folder-trust state, for display only:
//
//   - "na"        — no .marshal/config.toml, so trust changes nothing.
//   - "untrusted" — project config exists but no trust record grants it,
//     so marshal ignores that config and every project-scope plugin.
//   - "trusted"   — a trust record grants it.
//
// Trust can only be granted by opening the project in the TUI:
// trust.HeadlessResolver never extends or records trust (see
// internal/trust/headless.go), so nothing reachable from the web UI can
// escalate a project's privileges. This function only reports.
//
// The file is read directly rather than through internal/trust on
// purpose: the bridge's value is that it is an ordinary external ACP
// client, and importing marshal internals for a badge would give that up.
//
// Known limitation: marshal also requires the recorded config hash to
// match the current .marshal/config.toml, and this does not recompute
// that hash — duplicating the hashing rules here would be worse than the
// imprecision. A project whose config changed after being trusted
// therefore reports "trusted" while marshal refuses it. The common case
// this exists to catch — a project never opened in the TUI — is reported
// correctly. Every failure path fails closed, to "untrusted", so an
// unreadable store never claims a project's config is active.
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
