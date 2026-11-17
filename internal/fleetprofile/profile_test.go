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
	if len(f.Servers) != 0 {
		t.Fatalf("a missing file has servers %v, want none", f.Servers)
	}
	if !f.Local.Notify {
		t.Fatal("a missing file turns notifications off, want them on")
	}
	if f.Path != path {
		t.Fatalf("Path = %q, want %q", f.Path, path)
	}
}

func TestTheFileReadsServersAndLocal(t *testing.T) {
	path := writeFleet(t, `
[servers.alpha]
url = "https://fleet.example.com:8443"
token_cmd = "pass show fleet/alpha"

[servers.beta]
url = "http://127.0.0.1:7700"
notify = false

[local]
notify = false
`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Servers) != 2 {
		t.Fatalf("servers = %v, want alpha and beta", sortedNames(f.Servers))
	}

	alpha, ok := f.Servers["alpha"]
	if !ok {
		t.Fatalf("servers = %v, want alpha", sortedNames(f.Servers))
	}
	if alpha.Name != "alpha" || alpha.URL != "https://fleet.example.com:8443" || alpha.TokenCmd != "pass show fleet/alpha" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if !alpha.Notify {
		t.Fatal("alpha's absent notify reads false, want true")
	}

	beta, ok := f.Servers["beta"]
	if !ok {
		t.Fatalf("servers = %v, want beta", sortedNames(f.Servers))
	}
	if beta.URL != "http://127.0.0.1:7700" || beta.TokenCmd != "" || beta.CAFile != "" {
		t.Fatalf("beta = %+v", beta)
	}
	if beta.Notify {
		t.Fatal("beta's notify = false reads true")
	}

	if f.Local.Notify {
		t.Fatal("local.notify = false reads true")
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

func TestATokenFieldIsRefused(t *testing.T) {
	_, err := Load(writeFleet(t, `
[servers.home]
url = "https://fleet.example.com"
token = "s3cret"
`))
	want := "fleet.toml: servers.home.token: tokens are not kept in this file; run `marshal fleet login home` or set token_cmd"
	if err == nil {
		t.Fatal("a token field loaded, want a refusal")
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestUnknownFieldsAreRefused(t *testing.T) {
	cases := map[string]string{
		"a server's field": `
[servers.home]
url = "https://fleet.example.com"
default = true
`,
		"a top-level field": `
default = "home"

[servers.home]
url = "https://fleet.example.com"
`,
		"a server's table": `
[servers.home]
url = "https://fleet.example.com"

[servers.home.extra]
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

func TestServerNamesAreChecked(t *testing.T) {
	for _, name := range []string{"my server", "local"} {
		t.Run(name, func(t *testing.T) {
			body := "[servers.\"" + name + "\"]\nurl = \"https://fleet.example.com\"\n"
			_, err := Load(writeFleet(t, body))
			if err == nil {
				t.Fatalf("server name %q loaded, want a refusal", name)
			}
			if !strings.HasPrefix(err.Error(), "fleet.toml") {
				t.Fatalf("error = %q, want it to start with fleet.toml", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %q, want it to name %q", err, name)
			}
		})
	}

	path := writeFleet(t, `
[servers."fleet.example-2_eu"]
url = "https://fleet.example.com"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("a name of letters, digits, '.', '_' and '-' was refused: %v", err)
	}
}

func TestURLsMustBeOrigins(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https with a port", url: "https://fleet.example.com:8443"},
		{name: "http on localhost", url: "http://localhost:7700"},
		{name: "http on 127.0.0.1", url: "http://127.0.0.1:7700"},
		{name: "http on [::1]", url: "http://[::1]:7700"},
		{name: "a trailing slash", url: "https://fleet.example.com/"},
		{name: "http on another host", url: "http://fleet.example.com", wantErr: true},
		{name: "a path", url: "https://fleet.example.com/fleet", wantErr: true},
		{name: "a query", url: "https://fleet.example.com?token=x", wantErr: true},
		{name: "a fragment", url: "https://fleet.example.com#fleet", wantErr: true},
		{name: "user information", url: "https://user:pass@fleet.example.com", wantErr: true},
		{name: "no scheme", url: "fleet.example.com", wantErr: true},
		{name: "another scheme", url: "ftp://fleet.example.com", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeFleet(t, "[servers.home]\nurl = \""+c.url+"\"\n")
			f, err := Load(path)
			if c.wantErr {
				if err == nil {
					t.Fatalf("url %q loaded, want a refusal", c.url)
				}
				if !strings.HasPrefix(err.Error(), "fleet.toml") {
					t.Fatalf("error = %q, want it to start with fleet.toml", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("url %q refused: %v", c.url, err)
			}
			if got := f.Servers["home"].URL; got != c.url {
				t.Fatalf("url = %q, want %q", got, c.url)
			}
		})
	}
}

func TestACAFileIsResolvedAgainstTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "certs"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, fileName)
	body := `
[servers.relative]
url = "https://fleet.example.com"
ca_file = "certs/ca.pem"

[servers.absolute]
url = "https://fleet.example.com"
ca_file = "` + filepath.Join(dir, "certs", "other.pem") + `"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := f.Servers["relative"].CAFile, filepath.Join(dir, "certs", "ca.pem"); got != want {
		t.Fatalf("relative ca_file = %q, want %q", got, want)
	}
	if !filepath.IsAbs(f.Servers["relative"].CAFile) {
		t.Fatalf("ca_file = %q, want an absolute path", f.Servers["relative"].CAFile)
	}
	if got, want := f.Servers["absolute"].CAFile, filepath.Join(dir, "certs", "other.pem"); got != want {
		t.Fatalf("absolute ca_file = %q, want %q", got, want)
	}
}

func TestLookupNamesTheServers(t *testing.T) {
	f, err := Load(writeFleet(t, `
[servers.alpha]
url = "https://a.example.com"

[servers.beta]
url = "https://b.example.com"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	srv, err := f.Lookup("beta")
	if err != nil {
		t.Fatalf("Lookup(beta): %v", err)
	}
	if srv.URL != "https://b.example.com" {
		t.Fatalf("Lookup(beta) = %+v", srv)
	}

	_, err = f.Lookup("gamma")
	if err == nil {
		t.Fatal("Lookup(gamma) answered a server, want an error")
	}
	for _, want := range []string{f.Path, "alpha", "beta", "gamma"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

func TestLookupOnAnEmptyFileGivesAStarter(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), fileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = f.Lookup("gamma")
	if err == nil {
		t.Fatal("Lookup answered a server from an empty file, want an error")
	}
	for _, want := range []string{f.Path, "[servers.gamma]", "url = ", "token_cmd = "} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to hold %q", err, want)
		}
	}
}
