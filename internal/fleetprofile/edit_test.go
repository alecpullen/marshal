package fleetprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/credentials"
)

// readText answers the file at path as a string.
func readText(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// openStore is an OpenStore over an in-memory keychain.
func openStore(store credentials.Store) OpenStore {
	return func() (credentials.Store, error) { return store, nil }
}

// modeChecked reports whether this platform's file modes carry owner and group
// bits, as checkPrivate reads them.
func modeChecked(t *testing.T) bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return checkPrivate(path, info) != nil
}

func TestAddServerAppendsAndKeepsComments(t *testing.T) {
	body := `# servers I use

[servers.alpha]
url = "https://a.example.com"

[local]
notify = false
`
	path := writeFleet(t, body)
	srv := Server{Name: "beta", URL: "https://b.example.com:8443", CAFile: "/etc/ssl/beta.pem", Notify: true}

	if err := AddServer(path, srv); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	want := body + "\n[servers.beta]\nurl = \"https://b.example.com:8443\"\nca_file = \"/etc/ssl/beta.pem\"\n"
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sortedNames(f.Servers); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("servers = %v, want alpha and beta", got)
	}
	if got := f.Servers["alpha"].URL; got != "https://a.example.com" {
		t.Fatalf("alpha.url = %q, want the comment-kept file's", got)
	}
	beta := f.Servers["beta"]
	if beta.URL != srv.URL || beta.CAFile != srv.CAFile || beta.TokenCmd != "" {
		t.Fatalf("beta = %+v", beta)
	}
	if !beta.Notify {
		t.Fatal("beta's absent notify reads false, want true")
	}
	if f.Local.Notify {
		t.Fatal("local.notify = false was lost: it reads true")
	}
}

func TestAddServerCreatesAPrivateFile(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is already there: the test proves nothing", dir)
	}

	if err := AddServer(path, Server{Name: "home", URL: "https://fleet.example.com", Notify: true}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	if got, want := readText(t, path), "\n[servers.home]\nurl = \"https://fleet.example.com\"\n"; got != want {
		t.Fatalf("fleet.toml = %q, want %q", got, want)
	}
	if f, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	} else if f.Servers["home"].URL != "https://fleet.example.com" {
		t.Fatalf("Load = %+v", f.Servers)
	}

	if !modeChecked(t) {
		t.Log("this platform's file modes carry no owner or group bits")
		return
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("fleet.toml is mode %04o, want 0600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("%s is mode %04o, want 0700", dir, perm)
	}
}

func TestAddServerRefusesADuplicate(t *testing.T) {
	body := "[servers.home]\nurl = \"https://fleet.example.com\"\n"
	path := writeFleet(t, body)

	err := AddServer(path, Server{Name: "home", URL: "https://other.example.com", Notify: true})
	if err == nil {
		t.Fatal("a duplicate name was added, want a refusal")
	}
	if want := "home is already in fleet.toml"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if got := readText(t, path); got != body {
		t.Fatalf("the refused add changed the file:\n%s", got)
	}
}

func TestAddServerRefusesABadURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)

	err := AddServer(path, Server{Name: "home", URL: "http://fleet.example.com", Notify: true})
	if err == nil {
		t.Fatal("http on a host off this machine was added, want a refusal")
	}
	if !strings.HasPrefix(err.Error(), "fleet.toml: servers.home.url: ") {
		t.Fatalf("error = %q, want it to name the url", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused add created %s", path)
	}
}

func TestAddServerWritesNotifyOnlyWhenOff(t *testing.T) {
	path := writeFleet(t, "")

	if err := AddServer(path, Server{Name: "on", URL: "https://on.example.com", Notify: true}); err != nil {
		t.Fatalf("AddServer(on): %v", err)
	}
	if err := AddServer(path, Server{Name: "off", URL: "https://off.example.com", Notify: false}); err != nil {
		t.Fatalf("AddServer(off): %v", err)
	}

	want := `
[servers.on]
url = "https://on.example.com"

[servers.off]
url = "https://off.example.com"
notify = false
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !f.Servers["on"].Notify {
		t.Fatal("on's notify was written or lost: it reads false")
	}
	if f.Servers["off"].Notify {
		t.Fatal("off's notify = false was lost: it reads true")
	}
}

func TestRemoveServerDeletesOnlyItsTable(t *testing.T) {
	path := writeFleet(t, `# servers I use

[servers.alpha]
url = "https://a.example.com"

[servers.beta]
# beta's own
url = "https://b.example.com"

[servers.gamma]
url = "https://c.example.com"
`)

	if err := RemoveServer(path, "beta", openStore(credentials.NewMemStore())); err != nil {
		t.Fatalf("RemoveServer(beta): %v", err)
	}

	want := `# servers I use

[servers.alpha]
url = "https://a.example.com"

[servers.gamma]
url = "https://c.example.com"
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sortedNames(f.Servers); len(got) != 2 || got[0] != "alpha" || got[1] != "gamma" {
		t.Fatalf("servers = %v, want alpha and gamma", got)
	}
}

func TestRemoveServerAcceptsAQuotedHeader(t *testing.T) {
	path := writeFleet(t, `# mine

[servers."fleet.example-2"]  # the EU one
url = "https://eu.example.com"

[servers.home]
url = "https://home.example.com"
`)

	if err := RemoveServer(path, "fleet.example-2", openStore(credentials.NewMemStore())); err != nil {
		t.Fatalf("RemoveServer(fleet.example-2): %v", err)
	}

	want := `# mine

[servers.home]
url = "https://home.example.com"
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}
}

func TestRemoveServerRefusesAnInlineTable(t *testing.T) {
	body := "servers = { home = { url = \"https://fleet.example.com\" } }\n"
	path := writeFleet(t, body)

	err := RemoveServer(path, "home", neverOpen(t))
	if err == nil {
		t.Fatal("a server defined by an inline table was removed, want a refusal")
	}
	want := "home is not defined by its own [servers.home] table; edit " + path + " by hand"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if got := readText(t, path); got != body {
		t.Fatalf("the refused remove changed the file:\n%s", got)
	}
}

func TestRemoveServerDeletesTheToken(t *testing.T) {
	store := credentials.NewMemStore()
	for name := range map[string]bool{"home": true, "other": true} {
		if err := store.Set(KeychainKey(name), []byte("tok-"+name)); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	path := writeFleet(t, `[servers.home]
url = "https://home.example.com"

[servers.other]
url = "https://other.example.com"
`)

	if err := RemoveServer(path, "home", openStore(store)); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}

	if _, err := store.Get(KeychainKey("home")); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("home's token is still stored: %v", err)
	}
	if got, err := store.Get(KeychainKey("other")); err != nil || string(got) != "tok-other" {
		t.Fatalf("other's token = %q, %v, want it kept", got, err)
	}
	if f, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	} else if got := sortedNames(f.Servers); len(got) != 1 || got[0] != "other" {
		t.Fatalf("servers = %v, want other", got)
	}
}

func TestRemoveServerWithNoTokenSucceeds(t *testing.T) {
	store := credentials.NewMemStore()
	path := writeFleet(t, "[servers.home]\nurl = \"https://home.example.com\"\n")

	if err := RemoveServer(path, "home", openStore(store)); err != nil {
		t.Fatalf("RemoveServer with no stored token: %v", err)
	}
	if f, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	} else if len(f.Servers) != 0 {
		t.Fatalf("servers = %v, want none", sortedNames(f.Servers))
	}
}

func TestEditsRefuseAFileOthersCanRead(t *testing.T) {
	if !modeChecked(t) {
		t.Skip("this platform's file modes carry no owner or group bits")
	}
	body := "[servers.home]\nurl = \"https://fleet.example.com\"\n"
	path := writeFleetMode(t, body, 0o644)
	want := "fleet.toml at " + path + " is mode 0644: run chmod 600 " + path

	t.Run("AddServer", func(t *testing.T) {
		err := AddServer(path, Server{Name: "other", URL: "https://other.example.com", Notify: true})
		if err == nil {
			t.Fatal("a file others can read was added to, want a refusal")
		}
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
		if got := readText(t, path); got != body {
			t.Fatalf("the refused add changed the file:\n%s", got)
		}
	})

	t.Run("RemoveServer", func(t *testing.T) {
		err := RemoveServer(path, "home", neverOpen(t))
		if err == nil {
			t.Fatal("a file others can read was removed from, want a refusal")
		}
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
		if got := readText(t, path); got != body {
			t.Fatalf("the refused remove changed the file:\n%s", got)
		}
	})
}

// TestAddServerWritesARelay holds D20's edit: a relayed server is written with
// relay = "HUB" in place of url, and reads back with no url of its own.
func TestAddServerWritesARelay(t *testing.T) {
	body := "[servers.hub]\nurl = \"https://fleet.example.com\"\n"
	path := writeFleet(t, body)

	if err := AddServer(path, Server{Name: "edge", Relay: "hub", Notify: true}); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	want := body + "\n[servers.edge]\nrelay = \"hub\"\n"
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	edge := f.Servers["edge"]
	if edge.Relay != "hub" || edge.URL != "" {
		t.Fatalf("edge = %+v, want relay hub and no url", edge)
	}
	if !edge.Notify {
		t.Fatal("edge's absent notify reads false, want true")
	}

	// A relay names its hub, so one naming no server is refused, and the file
	// stays as the add before it wrote it.
	err = AddServer(path, Server{Name: "other", Relay: "nope", Notify: true})
	if err == nil {
		t.Fatal("a relay naming no server was added, want a refusal")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %q, want it to name the missing server", err)
	}
	if got := readText(t, path); got != want {
		t.Fatalf("the refused add changed the file:\n%s", got)
	}
}

// TestAddHostAppendsATableAndKeepsComments holds D6's edit: the file keeps its
// comments and gains the host's table at its end, private, and Load reads it
// back.
func TestAddHostAppendsATableAndKeepsComments(t *testing.T) {
	body := `# hosts I reach

[local]
notify = false
`
	path := writeFleet(t, body)
	h := Host{Name: "attic", SSH: "me@attic", Marshal: "/opt/marshal/bin/marshal", Notify: true}

	if err := AddHost(path, h); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	want := body + "\n[hosts.attic]\nssh = \"me@attic\"\nmarshal = \"/opt/marshal/bin/marshal\"\n"
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	attic, ok := f.Hosts["attic"]
	if !ok {
		t.Fatalf("hosts = %v, want attic", sortedNames(f.Hosts))
	}
	if attic.SSH != h.SSH || attic.Marshal != h.Marshal || !attic.Notify {
		t.Fatalf("attic = %+v", attic)
	}
	if f.Local.Notify {
		t.Fatal("local.notify = false was lost: it reads true")
	}

	if !modeChecked(t) {
		t.Log("this platform's file modes carry no owner or group bits")
		return
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("fleet.toml is mode %04o, want 0600", perm)
	}
}

// TestAddHostWritesMarshalAndNotifyOnlyWhenSet holds D6's table: marshal is
// written only when the host names it, and notify = false only when
// notifications are off.
func TestAddHostWritesMarshalAndNotifyOnlyWhenSet(t *testing.T) {
	path := writeFleet(t, "")

	if err := AddHost(path, Host{Name: "on", SSH: "me@on", Notify: true}); err != nil {
		t.Fatalf("AddHost(on): %v", err)
	}
	if err := AddHost(path, Host{Name: "off", SSH: "me@off", Marshal: "~/.local/bin/marshal", Notify: false}); err != nil {
		t.Fatalf("AddHost(off): %v", err)
	}

	want := `
[hosts.on]
ssh = "me@on"

[hosts.off]
ssh = "me@off"
marshal = "~/.local/bin/marshal"
notify = false
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !f.Hosts["on"].Notify {
		t.Fatal("on's notify was written or lost: it reads false")
	}
	if f.Hosts["off"].Notify {
		t.Fatal("off's notify = false was lost: it reads true")
	}
}

// TestAddHostRefusesANameAlreadyPresent holds D6's one-name-one-thing rule for
// the edit path too: a name the file holds as a host or as a server is
// refused.
func TestAddHostRefusesANameAlreadyPresent(t *testing.T) {
	cases := map[string]string{
		"in hosts":   "[hosts.attic]\nssh = \"me@attic\"\n",
		"in servers": "[servers.attic]\nurl = \"https://attic.example.com\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFleet(t, body)

			err := AddHost(path, Host{Name: "attic", SSH: "me@other", Notify: true})
			if err == nil {
				t.Fatal("a duplicate name was added, want a refusal")
			}
			if want := "attic is already in fleet.toml"; err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
			if got := readText(t, path); got != body {
				t.Fatalf("the refused add changed the file:\n%s", got)
			}
		})
	}
}

// TestRemoveHostDeletesOnlyItsTable holds D6's remove: the tables around it,
// and their comments, are kept.
func TestRemoveHostDeletesOnlyItsTable(t *testing.T) {
	path := writeFleet(t, `# hosts I reach

[hosts.attic]
# attic's own
ssh = "me@attic"

[hosts.basement]
ssh = "basement"

[hosts.shed]
ssh = "me@shed"
`)

	if err := RemoveHost(path, "basement"); err != nil {
		t.Fatalf("RemoveHost(basement): %v", err)
	}

	want := `# hosts I reach

[hosts.attic]
# attic's own
ssh = "me@attic"

[hosts.shed]
ssh = "me@shed"
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sortedNames(f.Hosts); len(got) != 2 || got[0] != "attic" || got[1] != "shed" {
		t.Fatalf("hosts = %v, want attic and shed", got)
	}
}
