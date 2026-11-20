// Package fleetprofile owns fleet.toml: the hosts the daemon reaches over SSH
// (one-daemon D6), and the file's [local] table.
//
// The file holds no secret, and is kept private all the same.
package fleetprofile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"

	"marshal/internal/app/config"
)

// File is fleet.toml (one-daemon D6).
type File struct {
	Path  string          // where it was read from
	Hosts map[string]Host // by name
	Local Local
}

// Host is one [hosts.<name>] table (one-daemon D6): a remote daemon reached
// by forwarding its socket over SSH.
type Host struct {
	Name    string
	SSH     string // the destination, exactly as ssh takes it
	Marshal string // the remote binary: absolute or "~/"-relative; "" is marshal on the remote PATH
	Notify  bool
}

// Local is the file's [local] table.
type Local struct {
	Notify bool
}

// fileName is the file's name in the user's config directory.
const fileName = "fleet.toml"

// hostNameRe is what a host's name may hold (D6).
var hostNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// rawFile, rawHost and rawLocal decode the file. They are separate from the
// types above so that decoding can be strict and a missing notify can read as
// true.
//
// Servers is kept only to refuse it: the retired [servers.<name>] tables are
// read as whatever they hold, so that a table with no keys is refused as
// readily as one with them (D6).
type rawFile struct {
	Servers map[string]any     `toml:"servers"`
	Hosts   map[string]rawHost `toml:"hosts"`
	Local   rawLocal           `toml:"local"`
}

// rawHost decodes one [hosts.<name>] table (D6). The retired server keys are
// pointers so that each can be refused by name: a host is reached over SSH,
// and has no url, TLS, relay or token of its own.
type rawHost struct {
	SSH      string  `toml:"ssh"`
	Marshal  string  `toml:"marshal"`
	Notify   *bool   `toml:"notify"`
	URL      *string `toml:"url"`
	Relay    *string `toml:"relay"`
	CAFile   *string `toml:"ca_file"`
	TokenCmd *string `toml:"token_cmd"`
	Token    *string `toml:"token"`
}

type rawLocal struct {
	Notify *bool `toml:"notify"`
}

// Path is fleet.toml in the user's config directory for home.
func Path(home string) string {
	return filepath.Join(config.UserDir(home), fileName)
}

// Load reads and checks the file at path (decisions 2 and 3). A missing file
// is the empty File, with notifications on.
func Load(path string) (File, error) {
	empty := File{Path: path, Hosts: map[string]Host{}, Local: Local{Notify: true}}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return empty, nil
		}
		return File{}, fmt.Errorf("fleet.toml at %s: %w", path, err)
	}
	if info.IsDir() {
		return File{}, fmt.Errorf("fleet.toml at %s is a directory", path)
	}
	// The mode and owner come first: a file others can read is refused
	// before its contents are trusted.
	if err := checkPrivate(path, info); err != nil {
		return File{}, err
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("fleet.toml at %s: %w", path, err)
	}
	return decode(path, src)
}

// decode reads src strictly and applies decision 2's rules.
func decode(path string, src []byte) (File, error) {
	var raw rawFile
	dec := toml.NewDecoder(bytes.NewReader(src)).DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return File{}, decodeError(err)
	}

	f := File{
		Path:  path,
		Hosts: make(map[string]Host, len(raw.Hosts)),
		Local: Local{Notify: true},
	}
	if raw.Local.Notify != nil {
		f.Local.Notify = *raw.Local.Notify
	}
	// The [servers.<name>] table is retired (D6): a file that still holds one
	// is refused, naming the first name in the order a reader meets them.
	if len(raw.Servers) > 0 {
		name := sortedNames(raw.Servers)[0]
		return File{}, fmt.Errorf("fleet.toml: servers.%s: server profiles are retired; a remote daemon is a host reached over SSH:\n\n[hosts.%s]\nssh = \"you@machine\"\n\n(docs/one-daemon/design.md D6)", name, name)
	}
	for _, name := range sortedNames(raw.Hosts) {
		h, err := buildHost(name, raw.Hosts[name])
		if err != nil {
			return File{}, err
		}
		f.Hosts[name] = h
	}
	return f, nil
}

// decodeError reports a decoding failure, naming the fields it is about.
func decodeError(err error) error {
	var strict *toml.StrictMissingError
	if errors.As(err, &strict) {
		keys := make([]string, 0, len(strict.Errors))
		for _, e := range strict.Errors {
			keys = append(keys, strings.Join(e.Key(), "."))
		}
		return fmt.Errorf("fleet.toml: unknown field %s", strings.Join(keys, ", "))
	}
	var derr *toml.DecodeError
	if errors.As(err, &derr) {
		return fmt.Errorf("fleet.toml: %s", derr.Error())
	}
	return fmt.Errorf("fleet.toml: %w", err)
}

// checkName applies D6's rule for a host's name: what it may hold, and what it
// may not mean.
func checkName(name string) error {
	if name == "local" {
		return fmt.Errorf("fleet.toml: hosts.local: \"local\" names the daemon's table, not a host; choose another name")
	}
	if !hostNameRe.MatchString(name) {
		return fmt.Errorf("fleet.toml: hosts.%s: a host's name may hold only letters, digits, '.', '_' and '-'", name)
	}
	// Dots alone pass the character rule but are a path segment in every
	// socket path the name builds: ".." would climb out of the daemon's hosts
	// directory. That is not a name.
	if name == "." || name == ".." {
		return fmt.Errorf("fleet.toml: hosts.%s: a host's name may not be dots alone", name)
	}
	return nil
}

// buildHost checks one [hosts.<name>] table and answers its host (D6): an SSH
// destination and, optionally, the remote binary's path.
func buildHost(name string, raw rawHost) (Host, error) {
	if err := checkName(name); err != nil {
		return Host{}, err
	}
	// A host is reached over SSH: the server keys say how it used to be
	// reached, so each is refused by name, in the order a reader meets them
	// in a server table.
	for _, retired := range []struct {
		key string
		set bool
	}{
		{"url", raw.URL != nil},
		{"relay", raw.Relay != nil},
		{"ca_file", raw.CAFile != nil},
		{"token_cmd", raw.TokenCmd != nil},
		{"token", raw.Token != nil},
	} {
		if retired.set {
			return Host{}, fmt.Errorf("fleet.toml: hosts.%s.%s: a host is reached over SSH and takes no %s; set ssh = \"you@machine\" (docs/one-daemon/design.md D6)", name, retired.key, retired.key)
		}
	}
	if raw.SSH == "" {
		return Host{}, fmt.Errorf("fleet.toml: hosts.%s.ssh: a host needs an ssh destination, as ssh takes it: \"you@machine\" or a Host from ~/.ssh/config", name)
	}
	// ssh reads a destination starting with "-" as one of its own options.
	if strings.HasPrefix(raw.SSH, "-") {
		return Host{}, fmt.Errorf("fleet.toml: hosts.%s.ssh: %q is read by ssh as an option; a destination never starts with \"-\"", name, raw.SSH)
	}
	// A destination is one argument: whitespace would split it, and a control
	// character has no place in one.
	for _, r := range raw.SSH {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return Host{}, fmt.Errorf("fleet.toml: hosts.%s.ssh: %q holds whitespace or a control character; a destination is one word", name, raw.SSH)
		}
	}
	if raw.Marshal != "" {
		if !filepath.IsAbs(raw.Marshal) && !strings.HasPrefix(raw.Marshal, "~/") {
			return Host{}, fmt.Errorf("fleet.toml: hosts.%s.marshal: %q is neither absolute nor under \"~/\"; write the remote binary's path, as \"/opt/marshal/bin/marshal\" or \"~/.local/bin/marshal\"", name, raw.Marshal)
		}
		for _, r := range raw.Marshal {
			if unicode.IsControl(r) {
				return Host{}, fmt.Errorf("fleet.toml: hosts.%s.marshal: %q holds a control character", name, raw.Marshal)
			}
		}
	}

	h := Host{Name: name, SSH: raw.SSH, Marshal: raw.Marshal, Notify: true}
	if raw.Notify != nil {
		h.Notify = *raw.Notify
	}
	return h, nil
}

// LookupHost answers the named host, or an error naming the file and the
// hosts it holds (D6).
func (f File) LookupHost(name string) (Host, error) {
	if h, ok := f.Hosts[name]; ok {
		return h, nil
	}
	names := sortedNames(f.Hosts)
	if len(names) == 0 {
		return Host{}, fmt.Errorf("fleet.toml at %s names no hosts; add one:\n\n[hosts.%s]\nssh = \"you@machine\"\n", f.Path, name)
	}
	return Host{}, fmt.Errorf("fleet.toml at %s has no host %q; it names %s", f.Path, name, strings.Join(names, ", "))
}

// sortedNames answers m's keys, sorted.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
