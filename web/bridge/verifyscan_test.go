package bridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verifyOf(t *testing.T, cfg string) VerifyCommands {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".marshal"), 0o700)
	os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte(cfg), 0o600)
	return readVerifyCommands(root)
}

func TestReadVerifyCommands(t *testing.T) {
	cases := map[string]struct {
		cfg  string
		want VerifyCommands
	}{
		"basic and literal": {"[models]\nbuild = \"nope\"\n\n[sdd.verify]\nbuild = \"go build ./...\"\ntest = 'go test ./...'\n\n[other]\ntest = \"nope\"\n",
			VerifyCommands{"go build ./...", "go test ./..."}},
		"trailing comments": {"[sdd.verify]\ntest = 'go test' # run it\nbuild = \"make\" # ok\n",
			VerifyCommands{"make", "go test"}},
		"header inside a multi-line basic string": {"[notes]\ntext = \"\"\"\n[sdd.verify]\nbuild = \"evil\"\n\"\"\"\n\n[sdd.verify]\nbuild = \"real\"\ntest = \"go test\"\n",
			VerifyCommands{"real", "go test"}},
		"header inside a multi-line literal string": {"[notes]\ntext = '''\n[sdd.verify]\ntest = \"evil\"\n'''\n[sdd.verify]\ntest = \"real\"\n",
			VerifyCommands{"", "real"}},
		"multi-line value with line continuation": {"[sdd.verify]\nbuild = \"\"\"\ngo build \\\n   ./...\"\"\"\ntest = \"t\"\n",
			VerifyCommands{"go build ./...", "t"}},
		"multi-line array hides headers": {"[x]\nlist = [\n  \"a\",\n  [1, 2],\n]\n[sdd.verify]\ntest = \"t\"\n",
			VerifyCommands{"", "t"}},
		"dotted keys":                  {"[sdd]\nverify.build = \"b\"\n[sdd.verify]\ntest = \"t\"\n", VerifyCommands{"b", "t"}},
		"top-level dotted":             {"sdd.verify.test = \"t\"\n", VerifyCommands{"", "t"}},
		"garbage after value is unset": {"[sdd.verify]\ntest = \"t\" junk\n", VerifyCommands{}},
		"inline table is unset":        {"[sdd]\nverify = { build = \"b\" }\n", VerifyCommands{}},
		"no section":                   {"[models]\nx = 1\n", VerifyCommands{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := verifyOf(t, c.cfg); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
	if got := readVerifyCommands(t.TempDir()); got != (VerifyCommands{}) {
		t.Fatalf("no config: %+v", got)
	}
}

func TestProjectHealthVerifyOnlyForATrustedProject(t *testing.T) {
	e := newWSSpawnEnv(t)
	home := fakeHome(t)
	root := projectWithConfig(t)
	os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte("[sdd.verify]\ntest = \"go test\"\n"), 0o600)
	if err := e.f.ws.AddProject(root); err != nil {
		t.Fatal(err)
	}
	h, err := e.f.ProjectHealth(context.Background(), root)
	if err != nil || h.Verify != (VerifyCommands{}) {
		t.Fatalf("untrusted: %+v %v", h.Verify, err)
	}
	writeTrustStore(t, home, `{"`+root+`":{"trusted":true}}`)
	h, err = e.f.ProjectHealth(context.Background(), root)
	if err != nil || h.Verify.Test != "go test" {
		t.Fatalf("trusted: %+v %v", h.Verify, err)
	}
}

func TestScanTOMLStringsNeverPanics(t *testing.T) {
	for _, cfg := range []string{
		"[sdd.verify]\ntest = \"a\\",
		"[sdd.verify]\ntest = \"a\\\nb\"\n",
		"[sdd.verify]\ntest = \"\"\"a\\",
		"[sdd.verify]\ntest = '''a",
		"[sdd.verify]\ntest = \"",
		"[sdd.verify]\ntest = [\"a\\",
		"[sdd.verify]\ntest =",
		"[",
		"=",
	} {
		scanTOMLStrings(cfg)
	}
	// Every prefix of a document full of awkward tokens, and random
	// splices of its pieces, must scan without panicking.
	pieces := []string{"[sdd.verify]", "[[a]]", "test", "build", " = ", "\"", "'", "\"\"\"", "'''", "\\", "\n", "#", "[", "]", "{", "}", "x", " ", "."}
	seed := uint32(1)
	next := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>8) % n
	}
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for k := next(40); k >= 0; k-- {
			b.WriteString(pieces[next(len(pieces))])
		}
		doc := b.String()
		for cut := 0; cut <= len(doc); cut++ {
			scanTOMLStrings(doc[:cut])
		}
	}
}
