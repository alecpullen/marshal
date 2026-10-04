package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func buildLines(f *Fleet, name string, n int) (lines []string, status string) {
	for _, ev := range f.buildLog.Tail(buildKey(name, n)) {
		var p map[string]any
		_ = json.Unmarshal(ev.Data, &p)
		if l, ok := p["line"].(string); ok {
			lines = append(lines, l)
		}
		if s, ok := p["status"].(string); ok {
			status = s
		}
	}
	return
}

func TestLayerDockerfiles(t *testing.T) {
	layers, err := layerDockerfiles(sampleDoc("svc"))
	if err != nil || len(layers) != 3 {
		t.Fatalf("layers = %d, %v", len(layers), err)
	}
	if got := layers[0].Dockerfile(""); got != "FROM debian:bookworm-slim\n" {
		t.Errorf("l1 = %q", got)
	}
	if got := layers[1].Dockerfile("p2"); !strings.Contains(got, "FROM p2") || !strings.Contains(got, "go.dev/dl/go1.23.4") || !strings.Contains(got, "ENV PATH=/usr/local/go/bin") {
		t.Errorf("l2 = %q", got)
	}
	if got := layers[2].Dockerfile("p3"); !strings.Contains(got, "apt-get install -y --no-install-recommends 'git'") {
		t.Errorf("l3 = %q", got)
	}

	doc := sampleDoc("svc")
	doc.Packages = WSPackages{Go: []string{"golang.org/x/tools/cmd/goimports@latest"}, Pip: []string{"requests>=2"}}
	layers, _ = layerDockerfiles(doc)
	l3 := layers[2].Dockerfile("p")
	if !strings.Contains(l3, "RUN go install 'golang.org/x/tools/cmd/goimports@latest'") || !strings.Contains(l3, "RUN pip install 'requests>=2'") || strings.Contains(l3, "apt-get") {
		t.Errorf("l3 = %q", l3)
	}

	doc.Packages = WSPackages{Apt: []string{"git; rm -rf /"}}
	if _, err := layerDockerfiles(doc); err == nil {
		t.Error("a shell metacharacter in a package name was accepted")
	}
	doc = sampleDoc("svc")
	doc.Workspace.Base = "debian\nRUN evil"
	if _, err := layerDockerfiles(doc); err == nil {
		t.Error("a newline in the base image was accepted")
	}
	doc.Workspace.Base = ""
	if _, err := layerDockerfiles(doc); err == nil {
		t.Error("an empty base was accepted")
	}
}

func TestLayerTagFoldsInTheParent(t *testing.T) {
	a := layerTag("svc", "l2", "h", "parent-a")
	b := layerTag("svc", "l2", "h", "parent-b")
	if a == b || !strings.HasPrefix(a, "marshal-ws/svc:l2-") {
		t.Fatalf("tags %q %q", a, b)
	}
}

func TestBuildWorkspaceFirstBuildAndCache(t *testing.T) {
	f, _, imgs := wsFleet(t)
	publishDoc(t, f, "svc", sampleDoc("svc"))
	if err := f.BuildWorkspace(ctlContext(t), "svc", 1); err != nil {
		t.Fatal(err)
	}
	// l1, l2, l3 and the labelled final layer, then the derive step.
	if got := imgs.builtTags(); len(got) != 5 {
		t.Fatalf("builds = %v", got)
	}
	m, _ := f.templates.Meta("svc")
	v := m.Versions[0]
	if v.BuildStatus != "ok" || !strings.HasPrefix(v.ImageTag, derivedTagPrefix) || v.SizeBytes != 12345 || v.BuildMs < 0 {
		t.Fatalf("version = %+v", v)
	}
	lines, status := buildLines(f, "svc", 1)
	if status != "ok" || len(lines) == 0 {
		t.Fatalf("log = %v status=%q", lines, status)
	}

	// Only the packages change: l1 and l2 are cached, l3 and the final
	// layer rebuild, and the derive still runs.
	v2 := sampleDoc("svc")
	v2.Packages.Apt = []string{"git", "jq"}
	f.templates.SaveDraft("svc", wsSrc(t, v2))
	f.templates.Publish("svc", "t")
	before := len(imgs.builtTags())
	if err := f.BuildWorkspace(ctlContext(t), "svc", 2); err != nil {
		t.Fatal(err)
	}
	now := imgs.builtTags()[before:]
	if len(now) != 2 || !strings.Contains(now[0], ":l3-") || !strings.HasSuffix(now[1], ":v2") {
		t.Fatalf("second build ran %v", now)
	}
	lines, _ = buildLines(f, "svc", 2)
	cached := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "cached ") {
			cached++
		}
	}
	if cached != 2 {
		t.Fatalf("cached lines = %d in %v", cached, lines)
	}
}

func TestBuildWorkspaceFailureRecordsFailed(t *testing.T) {
	f, _, imgs := wsFleet(t)
	imgs.failTag = ":l2-"
	publishDoc(t, f, "svc", sampleDoc("svc"))
	if err := f.BuildWorkspace(ctlContext(t), "svc", 1); err == nil {
		t.Fatal("a failed layer reported success")
	}
	m, _ := f.templates.Meta("svc")
	if m.Versions[0].BuildStatus != "failed" {
		t.Fatalf("status = %q", m.Versions[0].BuildStatus)
	}
	lines, status := buildLines(f, "svc", 1)
	if status != "failed" || !strings.Contains(strings.Join(lines, "\n"), "boom") {
		t.Fatalf("log = %v status=%q", lines, status)
	}
}

func TestBuildLogReplaysOverSSE(t *testing.T) {
	f, _, _ := wsFleet(t)
	publishDoc(t, f, "svc", sampleDoc("svc"))
	f.BuildWorkspace(ctlContext(t), "svc", 1)
	if len(f.buildLog.Tail(buildKey("svc", 1))) == 0 {
		t.Fatal("nothing logged")
	}
}

func TestBuildWorkspaceBusy(t *testing.T) {
	f, _, imgs := wsFleet(t)
	imgs.buildHit, imgs.buildGo = make(chan struct{}, 8), make(chan struct{})
	publishDoc(t, f, "a", sampleDoc("a"))
	publishDoc(t, f, "b", sampleDoc("b"))
	publishDoc(t, f, "c", sampleDoc("c"))

	if err := f.StartBuild("a", 1); err != nil {
		t.Fatal(err)
	}
	<-imgs.buildHit // a is mid-build
	if err := f.StartBuild("a", 1); !errors.Is(err, ErrBuildBusy) {
		t.Fatalf("second build of the same template = %v", err)
	}
	if err := f.StartBuild("b", 1); err != nil {
		t.Fatalf("a second template should get the second slot: %v", err)
	}
	<-imgs.buildHit
	if err := f.StartBuild("c", 1); !errors.Is(err, ErrBuildBusy) {
		t.Fatalf("third concurrent build = %v", err)
	}
	close(imgs.buildGo)
	waitFor(t, 5*time.Second, "builds to finish", func() bool {
		m, _ := f.templates.Meta("b")
		n, _ := f.templates.Meta("a")
		return m.Versions[0].BuildStatus == "ok" && n.Versions[0].BuildStatus == "ok"
	})
}
