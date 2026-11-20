// Package fleetprofile owns fleet.toml (P5 F7): the named server profiles the
// fleet screen can run on, their tokens, and the TLS configuration a profile
// names.
//
// The file is kept private, and never holds a token: a token comes from the
// profile's token_cmd or from the OS keychain (decisions 2 to 4 of
// docs/daemon/plans/2026-11-16-p5-7-server-profiles-plan.md).
package fleetprofile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"

	"marshal/internal/app/config"
)

// File is fleet.toml (P5 F7).
type File struct {
	Path    string            // where it was read from
	Servers map[string]Server // by name
	Hosts   map[string]Host   // by name
	Local   Local
}

// Server is one named server profile (decision 2).
type Server struct {
	Name     string
	URL      string // an origin: https, or http on a loopback host
	Relay    string // the server profile this one is reached through, or ""
	TokenCmd string
	CAFile   string // absolute once loaded
	Notify   bool
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

// serverNameRe is what a server's name may hold (decision 2).
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// rawFile, rawServer, rawHost and rawLocal decode the file. They are separate
// from the types above so that decoding can be strict and a missing notify can
// read as true.
type rawFile struct {
	Servers map[string]rawServer `toml:"servers"`
	Hosts   map[string]rawHost   `toml:"hosts"`
	Local   rawLocal             `toml:"local"`
}

type rawServer struct {
	URL      string  `toml:"url"`
	Relay    string  `toml:"relay"`
	TokenCmd string  `toml:"token_cmd"`
	CAFile   string  `toml:"ca_file"`
	Notify   *bool   `toml:"notify"`
	Token    *string `toml:"token"`
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
	empty := File{Path: path, Servers: map[string]Server{}, Hosts: map[string]Host{}, Local: Local{Notify: true}}

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
		Path:    path,
		Servers: make(map[string]Server, len(raw.Servers)),
		Hosts:   make(map[string]Host, len(raw.Hosts)),
		Local:   Local{Notify: true},
	}
	if raw.Local.Notify != nil {
		f.Local.Notify = *raw.Local.Notify
	}
	for _, name := range sortedNames(raw.Servers) {
		srv, err := buildServer(path, name, raw.Servers[name])
		if err != nil {
			return File{}, err
		}
		f.Servers[name] = srv
	}
	for _, name := range sortedNames(raw.Hosts) {
		h, err := buildHost(name, raw.Hosts[name])
		if err != nil {
			return File{}, err
		}
		f.Hosts[name] = h
	}
	// One name means one thing: a table is a host or a server, never both
	// (D6).
	for _, name := range sortedNames(f.Hosts) {
		if _, both := f.Servers[name]; both {
			return File{}, fmt.Errorf("fleet.toml: %s is both a server and a host; keep the [hosts.%s] table", name, name)
		}
	}
	// A relay names another server, so it is checked once they are all built
	// (D20).
	for _, name := range sortedNames(f.Servers) {
		if err := checkRelay(f.Servers, f.Servers[name]); err != nil {
			return File{}, err
		}
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

// buildServer checks one [servers.<name>] table and answers its profile.
func buildServer(path, name string, raw rawServer) (Server, error) {
	if raw.Token != nil {
		return Server{}, fmt.Errorf("fleet.toml: servers.%s.token: tokens are not kept in this file; run `marshal fleet login %s` or set token_cmd", name, name)
	}
	if err := checkName("servers", name); err != nil {
		return Server{}, err
	}
	if err := checkURLOrRelay(name, raw.URL, raw.Relay); err != nil {
		return Server{}, err
	}

	srv := Server{Name: name, URL: raw.URL, Relay: raw.Relay, TokenCmd: raw.TokenCmd, Notify: true}
	if raw.Notify != nil {
		srv.Notify = *raw.Notify
	}
	if raw.CAFile != "" {
		ca := raw.CAFile
		if !filepath.IsAbs(ca) {
			ca = filepath.Join(filepath.Dir(path), ca)
		}
		abs, err := filepath.Abs(ca)
		if err != nil {
			return Server{}, fmt.Errorf("fleet.toml: servers.%s.ca_file: %q: %w", name, raw.CAFile, err)
		}
		srv.CAFile = abs
	}
	return srv, nil
}

// checkName applies decision 2's rule for a name in table, which is "servers"
// or "hosts": what a table's name may hold, and what it may not mean.
func checkName(table, name string) error {
	noun := "server"
	if table == "hosts" {
		noun = "host"
	}
	if name == "local" {
		return fmt.Errorf("fleet.toml: %s.local: \"local\" names the daemon's table, not a %s; choose another name", table, noun)
	}
	if !serverNameRe.MatchString(name) {
		return fmt.Errorf("fleet.toml: %s.%s: a %s's name may hold only letters, digits, '.', '_' and '-'", table, name, noun)
	}
	// Dots alone pass the character rule but are a path segment in every URL
	// or socket path the name builds: ".." would climb out of
	// /api/hosts/<name> and answer from the relay's own API, or out of the
	// daemon's hosts directory. Neither is a name.
	if name == "." || name == ".." {
		return fmt.Errorf("fleet.toml: %s.%s: a %s's name may not be dots alone", table, name, noun)
	}
	return nil
}

// buildHost checks one [hosts.<name>] table and answers its host (D6): an SSH
// destination and, optionally, the remote binary's path.
func buildHost(name string, raw rawHost) (Host, error) {
	if err := checkName("hosts", name); err != nil {
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

// checkRelay applies D20's rules for a relayed server: its relay names a
// server that holds a url and is not itself relayed — there are no chains —
// and a relayed server carries no ca_file, because its TLS is the one it is
// reached through.
func checkRelay(servers map[string]Server, srv Server) error {
	if srv.Relay == "" {
		return nil
	}
	hub, ok := servers[srv.Relay]
	if !ok {
		return fmt.Errorf("fleet.toml: servers.%s.relay: fleet.toml names no server %s; it names %s", srv.Name, srv.Relay, strings.Join(sortedNames(servers), ", "))
	}
	if hub.Relay != "" {
		return fmt.Errorf("fleet.toml: servers.%s.relay: %s is itself relayed", srv.Name, srv.Relay)
	}
	if srv.CAFile != "" {
		return fmt.Errorf("fleet.toml: servers.%s.ca_file: a relayed server has no ca_file; its TLS is %s's", srv.Name, srv.Relay)
	}
	return nil
}

// checkURLOrRelay applies D20's rule that a server has a url or a relay, never
// both and never neither.
func checkURLOrRelay(name, rawURL, relay string) error {
	switch {
	case rawURL != "" && relay != "":
		return fmt.Errorf("fleet.toml: servers.%s.url: a relayed server has no url; it is reached through relay %q", name, relay)
	case rawURL == "" && relay == "":
		return fmt.Errorf("fleet.toml: servers.%s: a server needs a url, or a relay naming one", name)
	case relay != "":
		return nil
	}
	return checkURL(name, rawURL)
}

// checkURL applies decision 2's rule for a server's url: an origin, https, or
// http on a loopback host.
func checkURL(name, raw string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("fleet.toml: servers.%s.url: %s", name, fmt.Sprintf(format, args...))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("%q is not a URL: %v", raw, err)
	}
	switch {
	case u.Scheme == "":
		return bad("%q has no scheme: want https://host", raw)
	case u.Host == "":
		return bad("%q has no host", raw)
	case u.User != nil:
		return bad("%q carries user information; the token is not kept in the url", raw)
	case u.RawQuery != "" || u.ForceQuery:
		return bad("%q carries a query", raw)
	case u.Fragment != "":
		return bad("%q carries a fragment", raw)
	case u.Path != "" && u.Path != "/":
		return bad("%q carries a path; a server's url is an origin", raw)
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http":
		if !isLoopbackHost(u.Hostname()) {
			return bad("%q is http on %s, which is not loopback: use https", raw, u.Hostname())
		}
		return nil
	default:
		return bad("%q is %s, not https or http", raw, u.Scheme)
	}
}

// isLoopbackHost reports whether host names this machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Lookup answers the named server, or an error naming the file and what it
// holds (decision 9).
func (f File) Lookup(name string) (Server, error) {
	if srv, ok := f.Servers[name]; ok {
		return srv, nil
	}
	names := sortedNames(f.Servers)
	if len(names) == 0 {
		return Server{}, fmt.Errorf("fleet.toml at %s names no servers; add one:\n\n[servers.%s]\nurl = \"https://fleet.example.com\"\ntoken_cmd = \"pass show fleet/%s\"\n", f.Path, name, name)
	}
	return Server{}, fmt.Errorf("fleet.toml at %s has no server %q; it names %s", f.Path, name, strings.Join(names, ", "))
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
