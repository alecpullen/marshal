package postmortem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"marshal/internal/app/config"
)

// Dir returns the directory a project's postmortem reports live in:
// <user config dir>/postmortems/<project-slug>. Reports are harness data, so
// they stay with harness data — never in the user's repository.
// MARSHAL_CONFIG_DIR (honoured by config.UserDir) overrides the base, which
// is what the tests use to keep writes inside a temp dir.
func Dir(home, projectSlug string) string {
	return filepath.Join(config.UserDir(home), "postmortems", projectSlug)
}

// ProjectSlug derives the on-disk directory name for a project from its
// working directory: the base name, lowercased, with runs of non-alphanumeric
// runes collapsed to a single dash and leading/trailing dashes trimmed. An
// empty result falls back to "default" so a report always has a home.
func ProjectSlug(workingDir string) string {
	base := strings.ToLower(filepath.Base(workingDir))

	var b strings.Builder
	b.Grow(len(base))
	lastDash := false
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "default"
	}
	return slug
}

// SessionSlug derives the on-disk file name for a session id: runs of runes
// that are not a letter, digit, underscore, or dash collapse to a single dash
// and leading/trailing dashes are trimmed. Unlike ProjectSlug the case is
// preserved — a session id is a case-sensitive identifier, not a display name
// — and the file name is derived rather than a directory so path separators
// and traversal segments must never survive. An empty result, or one that is
// exactly "." or "..", falls back to "session".
//
// Exported so callers that need to name the report file (tests, future
// tooling) derive it the same way Write does rather than re-implementing it.
func SessionSlug(sessionID string) string {
	var b strings.Builder
	b.Grow(len(sessionID))
	lastDash := false
	for _, r := range sessionID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			b.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" || slug == "." || slug == ".." {
		return "session"
	}
	return slug
}

// Write persists report as <Dir(home, projectSlug)>/<sessionID>.json and
// returns the path written. The write is atomic: the JSON goes to a sibling
// temp file and is renamed into place, so an interrupted write can never leave
// a truncated report behind and a re-run overwrites idempotently.
func Write(report Report, home, projectSlug, sessionID string) (string, error) {
	dir := Dir(home, projectSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create postmortem dir: %w", err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal postmortem: %w", err)
	}
	data = append(data, '\n')

	// Sanitise the id before joining: a resumed/imported session row or an ACP
	// client-supplied session/load id can contain path separators, and the
	// project slug alone is not enough to keep the write inside the tree.
	name := SessionSlug(sessionID) + ".json"
	dest := filepath.Join(dir, name)
	tmp := dest + ".tmp"

	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write postmortem temp file: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		// Leave no debris behind on a failed rename.
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename postmortem into place: %w", err)
	}
	return dest, nil
}
