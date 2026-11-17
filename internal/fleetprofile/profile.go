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

	toml "github.com/pelletier/go-toml/v2"

	"marshal/internal/app/config"
)

// File is fleet.toml (P5 F7).
type File struct {
	Path    string            // where it was read from
	Servers map[string]Server // by name
	Local   Local
}

// Server is one named server profile (decision 2).
type Server struct {
	Name     string
	URL      string // an origin: https, or http on a loopback host
	TokenCmd string
	CAFile   string // absolute once loaded
	Notify   bool
}

// Local is the file's [local] table.
type Local struct {
	Notify bool
}

// fileName is the file's name in the user's config directory.
const fileName = "fleet.toml"

// serverNameRe is what a server's name may hold (decision 2).
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// rawFile, rawServer and rawLocal decode the file. They are separate from the
// types above so that decoding can be strict and a missing notify can read as
// true.
type rawFile struct {
	Servers map[string]rawServer `toml:"servers"`
	Local   rawLocal             `toml:"local"`
}

type rawServer struct {
	URL      string  `toml:"url"`
	TokenCmd string  `toml:"token_cmd"`
	CAFile   string  `toml:"ca_file"`
	Notify   *bool   `toml:"notify"`
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
	empty := File{Path: path, Servers: map[string]Server{}, Local: Local{Notify: true}}

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
	if err := checkName(name); err != nil {
		return Server{}, err
	}
	if err := checkURL(name, raw.URL); err != nil {
		return Server{}, err
	}

	srv := Server{Name: name, URL: raw.URL, TokenCmd: raw.TokenCmd, Notify: true}
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

// checkName applies decision 2's rule for a server's name.
func checkName(name string) error {
	if name == "local" {
		return errors.New("fleet.toml: servers.local: \"local\" names the daemon's table, not a server; choose another name")
	}
	if !serverNameRe.MatchString(name) {
		return fmt.Errorf("fleet.toml: servers.%s: a server's name may hold only letters, digits, '.', '_' and '-'", name)
	}
	return nil
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

// sortedNames answers m's keys, sorted.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
