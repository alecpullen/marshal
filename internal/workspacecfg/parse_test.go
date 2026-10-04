package workspacecfg

import (
	"strings"
	"testing"
)

const fullExample = `# a workspace
[workspace]
name = "api"
base = "debian:12"
toolchains = ["go@1.23", "node@20"]

[packages]
apt = ["git"]
go = ["golang.org/x/tools/cmd/goimports@latest"]

[[mounts]]
repo = "shared-lib"
target = "/libs/shared"
readonly = true

[[mounts]]
volume = "cache"
target = "/cache"

[files]
"conf/dev.env" = {target = "/etc/dev.env", readonly = true}

[secrets]
NPM_TOKEN = "vault:npm"

[secrets.inject]
"api.github.com" = {ref = "vault:gh", header = "Authorization", format = "Bearer {}"}

[network]
mode = "allowlist"
egress = ["github.com", "*.golang.org"]

[resources]
cpu = 2
memory = "4g"
disk = "10g"
timeout = "30m"

[policy]
mode = "edit"
allow = ["go test *"]

[setup]
run = "go mod download"
`

func errorsOf(d []Diagnostic) []Diagnostic {
	var out []Diagnostic
	for _, x := range d {
		if x.Severity == SeverityError {
			out = append(out, x)
		}
	}
	return out
}

func TestParseFullExample(t *testing.T) {
	d, secs, diags := Parse([]byte(fullExample))
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	if d.Workspace.Name != "api" || d.Workspace.Base != "debian:12" || len(d.Workspace.Toolchains) != 2 {
		t.Fatalf("workspace = %+v", d.Workspace)
	}
	if len(d.Mounts) != 2 || d.Mounts[0].Repo != "shared-lib" || !d.Mounts[0].Readonly || d.Mounts[1].Volume != "cache" {
		t.Fatalf("mounts = %+v", d.Mounts)
	}
	if d.Files["conf/dev.env"].Target != "/etc/dev.env" {
		t.Fatalf("files = %+v", d.Files)
	}
	if d.SecretsEnv["NPM_TOKEN"] != "vault:npm" {
		t.Fatalf("secretsEnv = %+v", d.SecretsEnv)
	}
	if got := d.Inject["api.github.com"]; got.Ref != "vault:gh" || got.Header != "Authorization" || got.Format != "Bearer {}" {
		t.Fatalf("inject = %+v", got)
	}
	if d.Network.Mode != "allowlist" || len(d.Network.Egress) != 2 {
		t.Fatalf("network = %+v", d.Network)
	}
	if d.Resources.CPU != 2 || d.Resources.Memory != "4g" || d.Resources.Timeout != "30m" {
		t.Fatalf("resources = %+v", d.Resources)
	}
	if d.Policy.Mode != "edit" || d.Policy.Allow[0] != "go test *" || d.Setup.Run != "go mod download" {
		t.Fatalf("policy/setup = %+v %+v", d.Policy, d.Setup)
	}
	layers := map[int]bool{}
	for _, s := range secs {
		layers[s.Layer] = true
	}
	for l := 0; l <= 9; l++ {
		if !layers[l] {
			t.Errorf("layer %d missing from sections %+v", l, secs)
		}
	}
}

func TestParseValidation(t *testing.T) {
	cases := map[string]string{
		"toolchain":   "[workspace]\ntoolchains = [\"ruby@3\"]\n",
		"network":     "[network]\nmode = \"wide\"\n",
		"secret":      "[secrets]\nTOKEN = \"plain\"\n",
		"inject":      "[secrets.inject]\n\"h\" = {ref = \"plain\"}\n",
		"both":        "[[mounts]]\nrepo = \"a\"\nvolume = \"b\"\ntarget = \"/x\"\n",
		"neither":     "[[mounts]]\ntarget = \"/x\"\n",
		"notarget":    "[[mounts]]\nrepo = \"a\"\n",
		"policy":      "[policy]\nmode = \"yolo\"\n",
		"memory":      "[resources]\nmemory = \"4gb\"\n",
		"disk":        "[resources]\ndisk = \"lots\"\n",
		"timeout":     "[resources]\ntimeout = \"soon\"\n",
		"secrettable": "[secrets]\n[secrets.other]\nx = 1\n",
		"secretint":   "[secrets]\nN = 5\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, diags := Parse([]byte(src))
			if len(errorsOf(diags)) == 0 {
				t.Fatalf("no error diagnostic for %q: %+v", src, diags)
			}
		})
	}
}

func TestParseValidationLine(t *testing.T) {
	src := "[workspace]\nname = \"x\"\n\n[network]\nmode = \"wide\"\n"
	_, _, diags := Parse([]byte(src))
	if len(diags) != 1 || diags[0].Line != 4 {
		t.Fatalf("diags = %+v, want one at line 4", diags)
	}
}

func TestParseUnknownKeyWarns(t *testing.T) {
	src := "[workspace]\nname = \"x\"\nbogus = 1\n\n[packages]\napt = [\"git\"]\n"
	d, _, diags := Parse([]byte(src))
	if len(diags) != 1 || diags[0].Severity != SeverityWarning || diags[0].Line != 3 {
		t.Fatalf("diags = %+v", diags)
	}
	if d.Workspace.Name != "x" || len(d.Packages.Apt) != 1 {
		t.Fatalf("rest did not decode: %+v", d)
	}
}

func TestParseSyntaxError(t *testing.T) {
	d, _, diags := Parse([]byte("[workspace\nname = 1\n"))
	if len(diags) != 1 || diags[0].Severity != SeverityError || diags[0].Line != 1 {
		t.Fatalf("diags = %+v", diags)
	}
	if d.Workspace.Name != "" {
		t.Fatalf("doc not empty: %+v", d)
	}
}

func TestParseSectionRanges(t *testing.T) {
	src := "[workspace]\nname = \"x\"\n\n[[mounts]]\nrepo = \"a\"\ntarget = \"/a\"\n\n[[mounts]]\nvolume = \"v\"\ntarget = \"/v\"\n\n\n[network]\nmode = \"off\"\n"
	_, secs, diags := Parse([]byte(src))
	if len(diags) != 0 {
		t.Fatalf("diags = %+v", diags)
	}
	find := func(layer int) (Section, bool) {
		for _, s := range secs {
			if s.Layer == layer {
				return s, true
			}
		}
		return Section{}, false
	}
	if s, _ := find(4); s.StartLine != 4 || s.EndLine != 10 {
		t.Errorf("mounts section = %+v, want 4..10", s)
	}
	if s, _ := find(7); s.StartLine != 13 || s.EndLine != 14 {
		t.Errorf("network section = %+v, want 13..14", s)
	}
	s1, _ := find(1)
	s2, _ := find(2)
	if s1 != (Section{1, "workspace", 1, 2}) || s2.StartLine != 1 || s2.EndLine != 2 || s2.Key != "workspace" {
		t.Errorf("workspace layers = %+v %+v", s1, s2)
	}
}

func TestParseEmpty(t *testing.T) {
	d, secs, diags := Parse([]byte(strings.Repeat("\n", 3)))
	if len(diags) != 0 || len(secs) != 0 || d.Mounts == nil || d.Files == nil {
		t.Fatalf("empty parse: %+v %+v %+v", d, secs, diags)
	}
}
