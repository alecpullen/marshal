package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseWSRef(t *testing.T) {
	cases := []struct {
		in   string
		want WSRef
		bad  bool
	}{
		{"go-service", WSRef{"studio", "go-service", 0}, false},
		{"go-service@3", WSRef{"studio", "go-service", 3}, false},
		{"repo:go-service", WSRef{"repo", "go-service", 0}, false},
		{"go-service@x", WSRef{}, true},
		{"go-service@0", WSRef{}, true},
		{"Bad Name", WSRef{}, true},
		{"repo:", WSRef{}, true},
	}
	for _, c := range cases {
		got, err := ParseWSRef(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("ParseWSRef(%q) = %+v, %v", c.in, got, err)
		}
	}
	if (WSRef{"studio", "a", 2}).String() != "a@2" || (WSRef{"repo", "a", 0}).String() != "repo:a" {
		t.Error("String does not round-trip")
	}
}

func publishDoc(t *testing.T, f *Fleet, name string, doc WSDoc) {
	t.Helper()
	if _, err := f.templates.Create(name, wsSrc(t, doc), "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.templates.Publish(name, "t"); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWorkspaceStudioLatestAndPinned(t *testing.T) {
	f, _, _ := wsFleet(t)
	publishDoc(t, f, "svc", sampleDoc("svc"))
	v2 := sampleDoc("svc")
	v2.Packages.Apt = []string{"git", "jq"}
	f.templates.SaveDraft("svc", wsSrc(t, v2))
	f.templates.Publish("svc", "t")

	latest, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "studio", Name: "svc"}, "")
	if err != nil || latest.Version != 2 || len(latest.Doc.Packages.Apt) != 2 || latest.Hash == "" {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	pinned, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "studio", Name: "svc", Version: 1}, "")
	if err != nil || pinned.Version != 1 || len(pinned.Doc.Packages.Apt) != 1 || pinned.Hash == latest.Hash {
		t.Fatalf("pinned = %+v, %v", pinned, err)
	}
	if _, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "studio", Name: "nope"}, ""); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestResolveWorkspaceErrorDiagnosticsFail(t *testing.T) {
	f, _, _ := wsFleet(t)
	f.templates.Create("bad", []byte("BAD toml"), "t")
	f.templates.Publish("bad", "t")
	if _, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "studio", Name: "bad"}, ""); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func writeRepoTemplate(t *testing.T, root, name string, src []byte) {
	t.Helper()
	dir := filepath.Join(root, ".marshal", "workspaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), src, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveWorkspaceRepoTemplateNeedsTrust(t *testing.T) {
	f, _, _ := wsFleet(t)
	home := fakeHome(t)
	root := projectWithConfig(t)
	overlay := sampleDoc("mine")
	writeRepoTemplate(t, root, "mine", wsSrc(t, overlay))

	ref := WSRef{Source: "repo", Name: "mine"}
	if _, err := f.ResolveWorkspace(ctlContext(t), ref, root); !errors.Is(err, ErrUntrustedRepoTemplate) {
		t.Fatalf("untrusted = %v", err)
	}
	writeTrustStore(t, home, `{"`+root+`":{"trusted":true}}`)
	got, err := f.ResolveWorkspace(ctlContext(t), ref, root)
	if err != nil || got.Source != "repo" || got.Doc.Workspace.Base != "debian:bookworm-slim" {
		t.Fatalf("trusted = %+v, %v", got, err)
	}
	if _, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "repo", Name: "absent"}, root); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("absent = %v", err)
	}
}

func TestResolveWorkspaceRepoExtendsStudio(t *testing.T) {
	f, _, _ := wsFleet(t)
	home := fakeHome(t)
	root := projectWithConfig(t)
	writeTrustStore(t, home, `{"`+root+`":{"trusted":true}}`)
	publishDoc(t, f, "svc", sampleDoc("svc"))
	overlay := WSDoc{
		Workspace: WSWorkspace{Name: "mine", Extends: "svc"},
		Packages:  WSPackages{Apt: []string{"jq"}},
		Setup:     WSSetup{Run: "make deps"},
	}
	writeRepoTemplate(t, root, "mine", wsSrc(t, overlay))
	got, err := f.ResolveWorkspace(ctlContext(t), WSRef{Source: "repo", Name: "mine"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if apt := got.Doc.Packages.Apt; len(apt) != 2 || apt[1] != "jq" {
		t.Fatalf("apt = %v", apt)
	}
	if got.Doc.Workspace.Base != "debian:bookworm-slim" || got.Doc.Workspace.Extends != "" || got.Doc.Setup.Run != "make deps" {
		t.Fatalf("doc = %+v", got.Doc)
	}
}

func TestMergeWS(t *testing.T) {
	base := WSDoc{
		Workspace:  WSWorkspace{Name: "svc", Base: "debian:12", Toolchains: []string{"go@1.23"}},
		Packages:   WSPackages{Apt: []string{"git"}},
		Mounts:     []WSMount{{Volume: "cache", Target: "/cache"}},
		Files:      map[string]WSFile{"a.conf": {Target: "/etc/a.conf"}},
		SecretsEnv: map[string]string{"TOKEN": "vault:t"},
		Inject:     map[string]WSInject{"github.com": {Ref: "vault:gh"}},
		Network:    WSNetwork{Mode: "allowlist", Egress: []string{"github.com"}},
		Resources:  WSResources{CPU: 4, Memory: "8g", Disk: "20g", Timeout: "2h"},
		Policy:     WSPolicy{Mode: "default", Allow: []string{"go test *"}},
		Setup:      WSSetup{Run: "studio"},
	}
	ok := WSDoc{
		Workspace:  WSWorkspace{Name: "mine", Base: "debian:12", Toolchains: []string{"go@1.23", "node@22"}},
		Packages:   WSPackages{Apt: []string{"git", "jq"}},
		Mounts:     []WSMount{{Volume: "more", Target: "/more"}},
		Files:      map[string]WSFile{"b.conf": {Target: "/etc/b.conf"}},
		Network:    WSNetwork{Mode: "off", Egress: []string{"example.com"}},
		Resources:  WSResources{CPU: 2, Memory: "4g", Disk: "10g", Timeout: "30m"},
		Policy:     WSPolicy{Allow: []string{"make *"}},
		Setup:      WSSetup{Run: "repo"},
		SecretsEnv: map[string]string{"TOKEN": "vault:t"},
	}
	got, err := mergeWS(base, ok)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Workspace.Toolchains) != 2 || len(got.Packages.Apt) != 2 || len(got.Mounts) != 2 || len(got.Files) != 2 {
		t.Fatalf("append rules: %+v", got)
	}
	if got.Network.Mode != "off" || len(got.Network.Egress) != 2 || got.Resources.Memory != "4g" || got.Resources.Timeout != "30m" || got.Resources.CPU != 2 {
		t.Fatalf("network/resources: %+v %+v", got.Network, got.Resources)
	}
	if got.Setup.Run != "studio\nrepo" || len(got.Policy.Allow) != 2 || got.Policy.Mode != "default" {
		t.Fatalf("setup/policy: %+v %+v", got.Setup, got.Policy)
	}

	violations := map[string]func(*WSDoc){
		"base":              func(d *WSDoc) { d.Workspace.Base = "alpine" },
		"mounts":            func(d *WSDoc) { d.Mounts = []WSMount{{Volume: "x", Target: "/cache"}} },
		"files":             func(d *WSDoc) { d.Files = map[string]WSFile{"z": {Target: "/cache"}} },
		"secrets":           func(d *WSDoc) { d.SecretsEnv = map[string]string{"NEW": "vault:n"} },
		"secrets.inject":    func(d *WSDoc) { d.Inject = map[string]WSInject{"evil.com": {Ref: "vault:gh"}} },
		"resources.memory":  func(d *WSDoc) { d.Resources.Memory = "16g" },
		"resources.disk":    func(d *WSDoc) { d.Resources.Disk = "40g" },
		"resources.timeout": func(d *WSDoc) { d.Resources.Timeout = "3h" },
		"resources.cpu":     func(d *WSDoc) { d.Resources.CPU = 8 },
		"network.mode":      func(d *WSDoc) { d.Network.Mode = "open" },
		"policy.mode":       func(d *WSDoc) { d.Policy.Mode = "acceptEdits" },
	}
	for field, mutate := range violations {
		o := WSDoc{}
		mutate(&o)
		_, err := mergeWS(base, o)
		var me ErrWorkspaceMerge
		if !errors.As(err, &me) || me.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
}

func TestParseWorkspaceCachesBySource(t *testing.T) {
	f, agent, _ := wsFleet(t)
	src := wsSrc(t, sampleDoc("svc"))
	for i := 0; i < 3; i++ {
		if _, _, _, err := f.parseWorkspace(ctlContext(t), src); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(agent.calls("workspace/parse")); n != 1 {
		t.Fatalf("workspace/parse called %d times, want 1", n)
	}
}
