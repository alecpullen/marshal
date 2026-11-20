package fleetprofile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AddHost appends h to the file at path as a [hosts.NAME] table (D6, D8):
// comments and order kept, the result checked as Load checks it, and written
// atomically at 0600. A name already in the file is refused.
func AddHost(path string, h Host) error {
	// buildHost is the check Load applies to the table this writes: the name,
	// the ssh destination and the marshal path, with no retired server key
	// set.
	if _, err := buildHost(h.Name, rawHost{SSH: h.SSH, Marshal: h.Marshal}); err != nil {
		return err
	}

	// Load checks the file the way it is read here: its privacy first, and a
	// missing file as no hosts at all.
	f, err := Load(path)
	if err != nil {
		return err
	}
	if _, ok := f.Hosts[h.Name]; ok {
		return fmt.Errorf("%s is already in fleet.toml", h.Name)
	}

	src, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		// The write below puts its temporary file beside the target, so the
		// directory is made first, private as the file it will hold.
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("fleet.toml at %s: %w", path, err)
		}
		src = nil
	default:
		return fmt.Errorf("fleet.toml at %s: %w", path, err)
	}

	out := appendHostTable(src, h)
	if _, err := decode(path, out); err != nil {
		return err
	}
	return writePrivate(path, out)
}

// RemoveHost deletes name's [hosts.NAME] table from the file at path (D6, D8).
// It touches no keychain: a host is reached by SSH, and holds no token.
func RemoveHost(path, name string) error {
	f, err := Load(path)
	if err != nil {
		return err
	}
	if _, err := f.LookupHost(name); err != nil {
		return err
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fleet.toml at %s: %w", path, err)
	}
	out, err := removeTable(path, name, src)
	if err != nil {
		return err
	}
	if _, err := decode(path, out); err != nil {
		return err
	}
	return writePrivate(path, out)
}

// tableHeaderRe is a [hosts.NAME] table header (D6, D8): the name bare or
// basic-quoted, spaces allowed inside the brackets, and an optional trailing
// comment.
var tableHeaderRe = regexp.MustCompile(`^\[\s*hosts\s*\.\s*(?:"([^"]*)"|([A-Za-z0-9._-]+))\s*\]\s*(?:#.*)?$`)

// tableHeader answers the host name a header line defines, if the line is one.
func tableHeader(line string) (name string, ok bool) {
	m := tableHeaderRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	// A bare key is never empty, so an empty quoted one is no table's header:
	// such a name does not load anyway (checkName).
	if m[2] != "" {
		return m[2], true
	}
	if m[1] == "" {
		return "", false
	}
	return m[1], true
}

// appendHostTable answers src with h's table written after it (D6): one blank
// line, then the header, ssh, marshal when set, and notify = false only when
// notifications are off.
func appendHostTable(src []byte, h Host) []byte {
	var b strings.Builder
	b.Write(src)
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n[hosts.%s]\n", headerKey(h.Name))
	fmt.Fprintf(&b, "ssh = %s\n", tomlString(h.SSH))
	if h.Marshal != "" {
		fmt.Fprintf(&b, "marshal = %s\n", tomlString(h.Marshal))
	}
	if !h.Notify {
		b.WriteString("notify = false\n")
	}
	return []byte(b.String())
}

// headerKey answers name as a table's key: bare when it needs no quoting, and
// quoted otherwise, because a bare '.' would make it a nested table.
func headerKey(name string) string {
	if strings.Contains(name, ".") {
		return tomlString(name)
	}
	return name
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// removeTable answers src without name's [hosts.NAME] table (D6, D8): from its
// header line to the next table header, or the end of the file.
func removeTable(path, name string, src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")

	start := -1
	for i, line := range lines {
		if got, ok := tableHeader(line); ok && got == name {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s is not defined by its own [hosts.%s] table; edit %s by hand", name, name, path)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}

	kept := append(lines[:start:start], lines[end:]...)
	return []byte(strings.Join(kept, "\n")), nil
}

// writePrivate writes src to path atomically at 0600: a temporary file in the
// same directory, then a rename (internal/app/config/gitignore.go:81).
func writePrivate(path string, src []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+fileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("fleet.toml at %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename lands

	if _, err := tmp.Write(src); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet.toml at %s: write %s: %w", path, tmpName, err)
	}
	// Flush to disk before the rename, so a crash cannot leave a truncated
	// fleet.toml in place of the original.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet.toml at %s: sync %s: %w", path, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fleet.toml at %s: close %s: %w", path, tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("fleet.toml at %s: chmod %s: %w", path, tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("fleet.toml at %s: rename %s: %w", path, tmpName, err)
	}
	return nil
}
