package fleetprofile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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

// TestAddHostCreatesAPrivateFile holds D6's rule for the file an add makes: the
// directory is made private with it.
func TestAddHostCreatesAPrivateFile(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is already there: the test proves nothing", dir)
	}

	if err := AddHost(path, Host{Name: "attic", SSH: "me@attic", Notify: true}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if got, want := readText(t, path), "\n[hosts.attic]\nssh = \"me@attic\"\n"; got != want {
		t.Fatalf("fleet.toml = %q, want %q", got, want)
	}
	if f, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	} else if f.Hosts["attic"].SSH != "me@attic" {
		t.Fatalf("Load = %+v", f.Hosts)
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
// the edit path: a name the file already holds is refused.
func TestAddHostRefusesANameAlreadyPresent(t *testing.T) {
	body := "[hosts.attic]\nssh = \"me@attic\"\n"
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

// TestRemoveHostAcceptsAQuotedHeader holds the header rule a remove relies on:
// a dotted name is a quoted key, and a trailing comment is no part of it.
func TestRemoveHostAcceptsAQuotedHeader(t *testing.T) {
	path := writeFleet(t, `# mine

[hosts."attic.example-2"]  # the attic one
ssh = "me@attic"

[hosts.basement]
ssh = "basement"
`)

	if err := RemoveHost(path, "attic.example-2"); err != nil {
		t.Fatalf("RemoveHost(attic.example-2): %v", err)
	}

	want := `# mine

[hosts.basement]
ssh = "basement"
`
	if got := readText(t, path); got != want {
		t.Fatalf("fleet.toml =\n%s\nwant\n%s", got, want)
	}
}

// TestRemoveHostRefusesAnInlineTable holds the remove's one refusal: a host
// written as an inline table has no [hosts.NAME] header to cut out.
func TestRemoveHostRefusesAnInlineTable(t *testing.T) {
	body := "hosts = { attic = { ssh = \"me@attic\" } }\n"
	path := writeFleet(t, body)

	err := RemoveHost(path, "attic")
	if err == nil {
		t.Fatal("a host defined by an inline table was removed, want a refusal")
	}
	want := "attic is not defined by its own [hosts.attic] table; edit " + path + " by hand"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if got := readText(t, path); got != body {
		t.Fatalf("the refused remove changed the file:\n%s", got)
	}
}

// TestEditsRefuseAFileOthersCanRead holds D6's privacy: the check comes before
// any edit, so a file others can read is neither added to nor cut down.
func TestEditsRefuseAFileOthersCanRead(t *testing.T) {
	if !modeChecked(t) {
		t.Skip("this platform's file modes carry no owner or group bits")
	}
	body := "[hosts.attic]\nssh = \"me@attic\"\n"
	path := writeFleetMode(t, body, 0o644)
	want := "fleet.toml at " + path + " is mode 0644: run chmod 600 " + path

	t.Run("AddHost", func(t *testing.T) {
		err := AddHost(path, Host{Name: "basement", SSH: "basement", Notify: true})
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

	t.Run("RemoveHost", func(t *testing.T) {
		err := RemoveHost(path, "attic")
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
