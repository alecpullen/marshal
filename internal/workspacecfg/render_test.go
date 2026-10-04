package workspacecfg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func mustPatch(t *testing.T, src string, layer int, v string) string {
	t.Helper()
	out, err := Patch([]byte(src), layer, json.RawMessage(v))
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	return string(out)
}

func TestPatchKeepsOtherSections(t *testing.T) {
	src := "# top comment\n[workspace]\nname = \"x\" # keep me\n\n[packages]\napt = [\"git\"]\n\n# net notes\n[network]\nmode = \"off\" # also me\n"
	out := mustPatch(t, src, 3, `{"apt":["git","curl"],"go":["a/b@v1"]}`)
	for _, want := range []string{"# top comment", `name = "x" # keep me`, "# net notes", `mode = "off" # also me`, `'curl'`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	d, _, diags := Parse([]byte(out))
	if len(diags) != 0 || len(d.Packages.Apt) != 2 || len(d.Packages.Go) != 1 {
		t.Fatalf("doc = %+v diags = %+v", d.Packages, diags)
	}
	// Bytes outside the packages section are untouched.
	if !strings.HasPrefix(out, "# top comment\n[workspace]\nname = \"x\" # keep me\n\n[packages]\n") ||
		!strings.HasSuffix(out, "\n\n# net notes\n[network]\nmode = \"off\" # also me\n") {
		t.Errorf("surroundings changed:\n%s", out)
	}
}

func TestPatchInsertsMissingNetwork(t *testing.T) {
	src := "[workspace]\nname = \"x\"\n\n[secrets]\nT = \"vault:t\"\n\n[setup]\nrun = \"make\"\n"
	out := mustPatch(t, src, 7, `{"mode":"allowlist","egress":["github.com"]}`)
	iSec := strings.Index(out, "[secrets]")
	iNet := strings.Index(out, "[network]")
	iSetup := strings.Index(out, "[setup]")
	if !(iSec < iNet && iNet < iSetup) {
		t.Fatalf("network not between secrets and setup:\n%s", out)
	}
	d, _, _ := Parse([]byte(out))
	if d.Network.Mode != "allowlist" || d.Setup.Run != "make" {
		t.Fatalf("doc = %+v", d)
	}

	// Without [secrets], it goes after the last earlier section.
	out = mustPatch(t, "[workspace]\nname = \"x\"\n\n[packages]\napt = [\"a\"]\n", 7, `{"mode":"off"}`)
	if strings.Index(out, "[packages]") > strings.Index(out, "[network]") {
		t.Fatalf("network before packages:\n%s", out)
	}
}

func TestPatchInsertsIntoEmptyAndBeforeFirst(t *testing.T) {
	out := mustPatch(t, "", 8, `{"memory":"2g"}`)
	if d, _, diags := Parse([]byte(out)); len(diags) != 0 || d.Resources.Memory != "2g" {
		t.Fatalf("out = %q", out)
	}
	out = mustPatch(t, "[setup]\nrun = \"x\"\n", 1, `{"name":"n","base":"b"}`)
	if !strings.HasPrefix(out, "[workspace]") || !strings.Contains(out, "[setup]") {
		t.Fatalf("workspace not first:\n%s", out)
	}
}

func TestPatchToolchainsKeepsBase(t *testing.T) {
	src := "[workspace]\nname = \"x\"\nbase = \"debian:12\"\ntoolchains = [\"go@1.22\"]\n\n[packages]\napt = [\"git\"]\n"
	out := mustPatch(t, src, 2, `{"name":"x","base":"debian:12","toolchains":["go@1.23","node@20"]}`)
	d, _, diags := Parse([]byte(out))
	if len(diags) != 0 {
		t.Fatalf("diags = %+v\n%s", diags, out)
	}
	if d.Workspace.Base != "debian:12" || !reflect.DeepEqual(d.Workspace.Toolchains, []string{"go@1.23", "node@20"}) {
		t.Fatalf("workspace = %+v", d.Workspace)
	}
	if !strings.Contains(out, "[packages]\napt = [\"git\"]") {
		t.Fatalf("packages disturbed:\n%s", out)
	}
}

func TestPatchSecretsAndFilesRanges(t *testing.T) {
	src := "[files]\n\"a/b\" = {target = \"/b\"}\n\n[secrets]\nA = \"vault:a\"\n\n[secrets.inject]\n\"h\" = {ref = \"vault:h\"}\n\n[network]\nmode = \"off\"\n"
	out := mustPatch(t, src, 6, `{"secretsEnv":{"B":"vault:b"},"inject":{}}`)
	d, _, diags := Parse([]byte(out))
	if len(diags) != 0 || len(d.SecretsEnv) != 1 || d.SecretsEnv["B"] != "vault:b" || len(d.Inject) != 0 {
		t.Fatalf("doc = %+v diags = %+v\n%s", d, diags, out)
	}
	if !strings.Contains(out, "[network]\nmode = \"off\"") || strings.Contains(out, "secrets.inject") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	out = mustPatch(t, src, 5, `{"x":{"target":"/x","readonly":true}}`)
	d, _, _ = Parse([]byte(out))
	if len(d.Files) != 1 || !d.Files["x"].Readonly || len(d.SecretsEnv) != 1 {
		t.Fatalf("files patch broke doc: %+v\n%s", d, out)
	}
}

func TestPatchEmptyValueRemovesSection(t *testing.T) {
	src := "[packages]\napt = [\"a\"]\n\n[network]\nmode = \"off\"\n"
	out := mustPatch(t, src, 3, `{}`)
	if strings.Contains(out, "[packages]") || !strings.Contains(out, "[network]") {
		t.Fatalf("out:\n%s", out)
	}
}

func TestPatchErrors(t *testing.T) {
	if _, err := Patch([]byte("[network\n"), 7, json.RawMessage(`{}`)); err == nil {
		t.Error("broken source should fail")
	}
	if _, err := Patch([]byte(""), 42, json.RawMessage(`{}`)); err == nil {
		t.Error("unknown layer should fail")
	}
	if _, err := Patch([]byte(""), 7, json.RawMessage(`"nope"`)); err == nil {
		t.Error("bad value should fail")
	}
}

func TestFormatRoundTrip(t *testing.T) {
	d, _, diags := Parse([]byte(fullExample))
	if len(diags) != 0 {
		t.Fatalf("diags = %+v", diags)
	}
	out := Format(d)
	d2, _, diags := Parse(out)
	if len(diags) != 0 {
		t.Fatalf("reparse diags = %+v\n%s", diags, out)
	}
	if !reflect.DeepEqual(d, d2) {
		t.Fatalf("round trip differs:\n%+v\n%+v\n%s", d, d2, out)
	}
	// Canonical order.
	last := -1
	for _, h := range []string{"[workspace]", "[packages]", "[[mounts]]", "[files", "[secrets]", "[network]", "[resources]", "[policy]", "[setup]"} {
		i := strings.Index(string(out), h)
		if i <= last {
			t.Fatalf("%s out of order in\n%s", h, out)
		}
		last = i
	}
	if got := Format(emptyDoc()); len(got) != 0 {
		t.Errorf("empty doc should format to nothing, got %q", got)
	}
}
