package fleetprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFleet writes body as fleet.toml in a fresh directory with mode 0600 and
// answers its path.
func writeFleet(t *testing.T, body string) string {
	t.Helper()
	return writeFleetMode(t, body, 0o600)
}

// writeFleetMode is writeFleet with an explicit mode.
func writeFleetMode(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

func TestPathIsFleetTomlInTheConfigDir(t *testing.T) {
	const home = "/home/someone"
	if got, want := Path(home), filepath.Join(home, ".config", "marshal", "fleet.toml"); got != want {
		t.Fatalf("Path(%q) = %q, want %q", home, got, want)
	}
}

func TestAMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), fileName)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if len(f.Hosts) != 0 {
		t.Fatalf("a missing file has hosts %v, want none", f.Hosts)
	}
	if !f.Local.Notify {
		t.Fatal("a missing file turns notifications off, want them on")
	}
	if f.Path != path {
		t.Fatalf("Path = %q, want %q", f.Path, path)
	}
}

func TestTheFileReadsLocal(t *testing.T) {
	path := writeFleet(t, `
[local]
notify = false
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Local.Notify {
		t.Fatal("local.notify = false reads true")
	}
	if len(f.Hosts) != 0 {
		t.Fatalf("hosts = %v, want none", sortedNames(f.Hosts))
	}
}

func TestAnAbsentLocalNotifyReadsTrue(t *testing.T) {
	path := writeFleet(t, `
[local]
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !f.Local.Notify {
		t.Fatal("an absent local.notify reads false, want true")
	}
}

func TestUnknownFieldsAreRefused(t *testing.T) {
	cases := map[string]string{
		"a host's field": `
[hosts.attic]
ssh = "me@attic"
default = true
`,
		"a top-level field": `
default = "attic"

[hosts.attic]
ssh = "me@attic"
`,
		"a host's table": `
[hosts.attic]
ssh = "me@attic"

[hosts.attic.extra]
key = "value"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeFleet(t, body))
			if err == nil {
				t.Fatal("an unknown field loaded, want a refusal")
			}
			if !strings.HasPrefix(err.Error(), "fleet.toml") {
				t.Fatalf("error = %q, want it to start with fleet.toml", err)
			}
			if !strings.Contains(err.Error(), "default") && !strings.Contains(err.Error(), "extra") {
				t.Fatalf("error = %q, want it to name the unknown field", err)
			}
		})
	}
}

func TestHostNamesAreChecked(t *testing.T) {
	for _, name := range []string{"my server", "local"} {
		t.Run(name, func(t *testing.T) {
			body := "[hosts.\"" + name + "\"]\nssh = \"me@attic\"\n"
			_, err := Load(writeFleet(t, body))
			if err == nil {
				t.Fatalf("host name %q loaded, want a refusal", name)
			}
			if !strings.HasPrefix(err.Error(), "fleet.toml") {
				t.Fatalf("error = %q, want it to start with fleet.toml", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %q, want it to name %q", err, name)
			}
			if !strings.Contains(err.Error(), "hosts.") {
				t.Fatalf("error = %q, want it to name the hosts table", err)
			}
		})
	}

	path := writeFleet(t, `
[hosts."attic.example-2_eu"]
ssh = "me@attic"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("a name of letters, digits, '.', '_' and '-' was refused: %v", err)
	}
}

// TestAHostNameIsNotDotsAlone holds D6's rule that a name is a path segment in
// the daemon's hosts directory: dots alone are not a name.
func TestAHostNameIsNotDotsAlone(t *testing.T) {
	for _, name := range []string{".", ".."} {
		_, err := Load(writeFleet(t, "[hosts.\""+name+"\"]\nssh = \"me@attic\"\n"))
		if err == nil {
			t.Fatalf("a host named %q was loaded, want a refusal", name)
		}
		if want := "dots alone"; !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to hold %q", err, want)
		}
	}
}

// TestAServersTableIsRetired holds D6's refusal of the retired server table:
// the message names the host table that replaces it.
func TestAServersTableIsRetired(t *testing.T) {
	_, err := Load(writeFleet(t, `
[servers.studio]
url = "https://studio.example.com"
`))
	if err == nil {
		t.Fatal("a servers table loaded, want a refusal")
	}
	for _, want := range []string{"[hosts.studio]", `ssh = "you@machine"`, "docs/one-daemon/design.md D6"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to hold %q", err, want)
		}
	}
}

// TestAServersTableIsRetiredEvenWhenEmptyOfKeys holds D6's refusal for a table
// that holds nothing: a reader who wrote the table has written a server.
func TestAServersTableIsRetiredEvenWhenEmptyOfKeys(t *testing.T) {
	_, err := Load(writeFleet(t, "[servers.x]\n"))
	if err == nil {
		t.Fatal("an empty servers table loaded, want a refusal")
	}
	for _, want := range []string{"servers.x", "[hosts.x]", `ssh = "you@machine"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to hold %q", err, want)
		}
	}
}

// TestAHostTableLoads holds D6's [hosts.<name>] table: ssh and marshal are
// read, and notifications are on unless the table turns them off.
func TestAHostTableLoads(t *testing.T) {
	f, err := Load(writeFleet(t, `
[hosts.attic]
ssh = "me@attic"

[hosts.basement]
ssh = "basement"
marshal = "/opt/marshal/bin/marshal"
notify = false
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := sortedNames(f.Hosts); len(got) != 2 || got[0] != "attic" || got[1] != "basement" {
		t.Fatalf("hosts = %v, want attic and basement", got)
	}

	attic, ok := f.Hosts["attic"]
	if !ok {
		t.Fatalf("hosts = %v, want attic", sortedNames(f.Hosts))
	}
	if attic.Name != "attic" || attic.SSH != "me@attic" || attic.Marshal != "" {
		t.Fatalf("attic = %+v", attic)
	}
	if !attic.Notify {
		t.Fatal("attic's absent notify reads false, want true")
	}

	basement := f.Hosts["basement"]
	if basement.SSH != "basement" || basement.Marshal != "/opt/marshal/bin/marshal" {
		t.Fatalf("basement = %+v", basement)
	}
	if basement.Notify {
		t.Fatal("basement's notify = false reads true")
	}
}

// TestAHostNeedsAnSSHDestination holds D6's rule that ssh is what a host is.
func TestAHostNeedsAnSSHDestination(t *testing.T) {
	_, err := Load(writeFleet(t, `
[hosts.attic]
marshal = "~/.local/bin/marshal"
`))
	if err == nil {
		t.Fatal("a host with no ssh destination loaded, want a refusal")
	}
	want := "fleet.toml: hosts.attic.ssh: a host needs an ssh destination, as ssh takes it: \"you@machine\" or a Host from ~/.ssh/config"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

// TestAHostDestinationMayNotLookLikeAnOption holds D6's rule that ssh never
// reads the destination as one of its own options.
func TestAHostDestinationMayNotLookLikeAnOption(t *testing.T) {
	_, err := Load(writeFleet(t, "[hosts.attic]\nssh = \"-oProxyCommand=x\"\n"))
	if err == nil {
		t.Fatal("a destination starting with \"-\" loaded, want a refusal")
	}
	for _, want := range []string{"hosts.attic.ssh", "option", `never starts with "-"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestAHostDestinationMayNotHoldWhitespace holds D6's rule that a destination
// is one argument.
func TestAHostDestinationMayNotHoldWhitespace(t *testing.T) {
	// "\t" in a TOML basic string is a tab.
	cases := map[string]string{
		"a space": "me@box extra",
		"a tab":   `me@box\textra`,
	}
	for name, ssh := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeFleet(t, "[hosts.attic]\nssh = \""+ssh+"\"\n"))
			if err == nil {
				t.Fatalf("destination %q loaded, want a refusal", ssh)
			}
			for _, want := range []string{"hosts.attic.ssh", "whitespace"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// TestAHostsMarshalIsAbsoluteOrHome holds D6's rule for marshal: the remote
// binary's path, absolute or relative to the remote home.
func TestAHostsMarshalIsAbsoluteOrHome(t *testing.T) {
	path := writeFleet(t, `
[hosts.absolute]
ssh = "me@attic"
marshal = "/opt/marshal/bin/marshal"

[hosts.home]
ssh = "me@attic"
marshal = "~/.local/bin/marshal"
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := f.Hosts["absolute"].Marshal, "/opt/marshal/bin/marshal"; got != want {
		t.Fatalf("marshal = %q, want %q", got, want)
	}
	if got, want := f.Hosts["home"].Marshal, "~/.local/bin/marshal"; got != want {
		t.Fatalf("marshal = %q, want %q", got, want)
	}

	_, err = Load(writeFleet(t, "[hosts.path]\nssh = \"me@attic\"\nmarshal = \"bin/marshal\"\n"))
	if err == nil {
		t.Fatal("a bare relative marshal loaded, want a refusal")
	}
	for _, want := range []string{"hosts.path.marshal", "bin/marshal"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestAHostRefusesTheServerKeys holds D6's rule that a host takes none of the
// server's keys, and that the refusal names the new shape.
func TestAHostRefusesTheServerKeys(t *testing.T) {
	cases := map[string]string{
		"url":       `url = "https://attic.example.com"`,
		"relay":     `relay = "hub"`,
		"ca_file":   `ca_file = "/etc/ssl/attic.pem"`,
		"token_cmd": `token_cmd = "pass show fleet/attic"`,
		"token":     `token = "s3cret"`,
	}
	for key, line := range cases {
		t.Run(key, func(t *testing.T) {
			_, err := Load(writeFleet(t, "[hosts.attic]\nssh = \"me@attic\"\n"+line+"\n"))
			if err == nil {
				t.Fatalf("hosts.attic took %s, want a refusal", key)
			}
			for _, want := range []string{"hosts.attic." + key, `ssh = "you@machine"`} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// TestLookupHostNamesTheFileAndItsHosts holds D6's lookup: the error names the
// file and the hosts it holds, and an empty file shows a starter table.
func TestLookupHostNamesTheFileAndItsHosts(t *testing.T) {
	f, err := Load(writeFleet(t, `
[hosts.attic]
ssh = "me@attic"

[hosts.basement]
ssh = "basement"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	h, err := f.LookupHost("basement")
	if err != nil {
		t.Fatalf("LookupHost(basement): %v", err)
	}
	if h.SSH != "basement" {
		t.Fatalf("LookupHost(basement) = %+v", h)
	}

	_, err = f.LookupHost("shed")
	if err == nil {
		t.Fatal("LookupHost(shed) answered a host, want an error")
	}
	for _, want := range []string{f.Path, "attic", "basement", "shed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}

	empty, err := Load(filepath.Join(t.TempDir(), fileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = empty.LookupHost("shed")
	if err == nil {
		t.Fatal("LookupHost answered a host from an empty file, want an error")
	}
	for _, want := range []string{empty.Path, "[hosts.shed]", `ssh = "you@machine"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to hold %q", err, want)
		}
	}
}
