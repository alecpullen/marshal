package fleetprofile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"marshal/internal/credentials"
)

// AddServer appends s to the file at path as a [servers.NAME] table (D6):
// comments and order kept, the result checked as Load checks it, and written
// atomically at 0600. A name already present is refused.
func AddServer(path string, s Server) error {
	if err := checkName("servers", s.Name); err != nil {
		return err
	}
	if err := checkURLOrRelay(s.Name, s.URL, s.Relay); err != nil {
		return err
	}
	if s.Relay != "" && s.CAFile != "" {
		// D20: a relayed server's TLS is the one it is reached through.
		return fmt.Errorf("fleet.toml: servers.%s.ca_file: a relayed server has no ca_file; its TLS is %s's", s.Name, s.Relay)
	}
	if s.TokenCmd != "" {
		return fmt.Errorf("fleet.toml: servers.%s.token_cmd: the Studio adds no token_cmd profile; edit %s by hand", s.Name, path)
	}

	// Load checks the file the way it is read here: its privacy first, and a
	// missing file as no servers at all.
	f, err := Load(path)
	if err != nil {
		return err
	}
	if _, ok := f.Servers[s.Name]; ok {
		return fmt.Errorf("%s is already in fleet.toml", s.Name)
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

	out := appendTable(src, s)
	if _, err := decode(path, out); err != nil {
		return err
	}
	return writePrivate(path, out)
}

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
	if _, ok := f.Servers[h.Name]; ok {
		return fmt.Errorf("%s is already in fleet.toml", h.Name)
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

// RemoveServer deletes name's table from the file at path, and the token
// open's keychain holds for it (D6). A server defined other than by its own
// table is refused.
func RemoveServer(path, name string, open OpenStore) error {
	f, err := Load(path)
	if err != nil {
		return err
	}
	if _, err := f.Lookup(name); err != nil {
		return err
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fleet.toml at %s: %w", path, err)
	}
	out, err := removeTable(path, "servers", name, src)
	if err != nil {
		return err
	}
	if _, err := decode(path, out); err != nil {
		return err
	}
	if err := writePrivate(path, out); err != nil {
		return err
	}
	return deleteToken(path, name, open)
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
	out, err := removeTable(path, "hosts", name, src)
	if err != nil {
		return err
	}
	if _, err := decode(path, out); err != nil {
		return err
	}
	return writePrivate(path, out)
}

// tableHeaderRe is a table header naming one server or one host (decision 1):
// the name bare or basic-quoted, spaces allowed inside the brackets, and an
// optional trailing comment.
var tableHeaderRe = regexp.MustCompile(`^\[\s*(servers|hosts)\s*\.\s*(?:"([^"]*)"|([A-Za-z0-9._-]+))\s*\]\s*(?:#.*)?$`)

// tableHeader answers the table and name a header line defines, if the line is
// one.
func tableHeader(line string) (table, name string, ok bool) {
	m := tableHeaderRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", "", false
	}
	// A bare key is never empty, so an empty quoted one is no table's header:
	// such a name does not load anyway (checkName).
	if m[3] != "" {
		return m[1], m[3], true
	}
	if m[2] == "" {
		return "", "", false
	}
	return m[1], m[2], true
}

// appendTable answers src with s's table written after it (decision 2): one
// blank line, then the header, url or relay, ca_file when set, and notify =
// false only when notifications are off. No token_cmd is written.
func appendTable(src []byte, s Server) []byte {
	var b strings.Builder
	b.Write(src)
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n[servers.%s]\n", headerKey(s.Name))
	if s.Relay != "" {
		// A relayed server names where it is reached, in place of its url
		// (D20).
		fmt.Fprintf(&b, "relay = %s\n", tomlString(s.Relay))
	} else {
		fmt.Fprintf(&b, "url = %s\n", tomlString(s.URL))
	}
	if s.CAFile != "" {
		// As given: a relative path stays relative to the file.
		fmt.Fprintf(&b, "ca_file = %s\n", tomlString(s.CAFile))
	}
	if !s.Notify {
		b.WriteString("notify = false\n")
	}
	return []byte(b.String())
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

// removeTable answers src without name's table in table (decision 1): from its
// header line to the next table header, or the end of the file.
func removeTable(path, table, name string, src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")

	start := -1
	for i, line := range lines {
		if gotTable, got, ok := tableHeader(line); ok && gotTable == table && got == name {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("%s is not defined by its own [%s.%s] table; edit %s by hand", name, table, name, path)
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

// deleteToken deletes name's keychain entry through open (D6). A missing entry
// is success, and a store that cannot be opened is reported: the file has been
// written by then, so what failed is named as the token alone.
func deleteToken(path, name string, open OpenStore) error {
	if open == nil {
		open = func() (credentials.Store, error) { return credentials.New(keychainService) }
	}
	store, err := open()
	switch {
	case err != nil:
		return fmt.Errorf("removed %s from %s, but its token: %w", name, path, err)
	case store == nil:
		return fmt.Errorf("removed %s from %s, but its token: this system has no keychain", name, path)
	}
	if err := store.Delete(KeychainKey(name)); err != nil {
		return fmt.Errorf("removed %s from %s, but its token: %w", name, path, err)
	}
	return nil
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
