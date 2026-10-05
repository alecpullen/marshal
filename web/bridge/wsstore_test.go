package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateStoreLifecycle(t *testing.T) {
	s := NewTemplateStore(t.TempDir())
	if _, err := s.Create("go-service", []byte("a = 1\n"), "alec"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("go-service", nil, "alec"); !errors.Is(err, ErrTemplateExists) {
		t.Fatalf("duplicate create = %v", err)
	}
	if err := s.SaveDraft("go-service", []byte("a = 2\n")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read("go-service", 0); string(got) != "a = 2\n" {
		t.Fatalf("draft = %q", got)
	}
	v, err := s.Publish("go-service", "alec")
	if err != nil || v.N != 1 || v.BuildStatus != "pending" || v.SHA256 == "" {
		t.Fatalf("publish = %+v, %v", v, err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Published != 1 || list[0].OwnerID != DefaultOwnerID {
		t.Fatalf("list = %+v", list)
	}
	if err := s.SetBuild("go-service", 1, "ok", "tag", 99, 1500); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Meta("go-service")
	if v := m.Versions[0]; v.BuildStatus != "ok" || v.ImageTag != "tag" || v.SizeBytes != 99 || v.BuildMs != 1500 {
		t.Fatalf("version = %+v", v)
	}
	if err := s.SetPool("go-service", 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPool("go-service", 5); !errors.Is(err, ErrTemplatePool) {
		t.Fatalf("pool 5 = %v", err)
	}
	if err := s.Delete("go-service", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta("go-service"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestTemplateStorePublishTwiceKeepsVersionsImmutable(t *testing.T) {
	s := NewTemplateStore(t.TempDir())
	s.Create("x", []byte("one"), "a")
	s.Publish("x", "a")
	s.SaveDraft("x", []byte("two"))
	v2, err := s.Publish("x", "a")
	if err != nil || v2.N != 2 {
		t.Fatalf("v2 = %+v, %v", v2, err)
	}
	s.SaveDraft("x", []byte("three"))
	if got, _ := s.Read("x", 1); string(got) != "one" {
		t.Fatalf("v1 changed: %q", got)
	}
	if got, _ := s.Read("x", 2); string(got) != "two" {
		t.Fatalf("v2 changed: %q", got)
	}
	if _, err := s.Read("x", 3); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("v3 = %v", err)
	}
}

func TestTemplateStoreDeleteRefusedWhileInUse(t *testing.T) {
	s := NewTemplateStore(t.TempDir())
	s.Create("x", nil, "a")
	if err := s.Delete("x", func(string) bool { return true }); !errors.Is(err, ErrTemplateInUse) {
		t.Fatalf("delete in use = %v", err)
	}
	if _, err := s.Meta("x"); err != nil {
		t.Fatalf("template gone after refused delete: %v", err)
	}
}

func TestTemplateStoreRejectsBadNames(t *testing.T) {
	s := NewTemplateStore(t.TempDir())
	for _, name := range []string{"", "Go", "-x", "a/b", "../x", "a b", strings.Repeat("a", 42)} {
		if _, err := s.Create(name, nil, "a"); !errors.Is(err, ErrTemplateName) {
			t.Errorf("Create(%q) = %v", name, err)
		}
		if _, err := s.Read(name, 0); !errors.Is(err, ErrTemplateName) {
			t.Errorf("Read(%q) = %v", name, err)
		}
	}
}

func TestTemplateStoreWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	s := NewTemplateStore(dir)
	s.Create("x", []byte("original"), "a")
	s.rename = func(string, string) error { return errors.New("disk on fire") }
	if err := s.SaveDraft("x", []byte("replacement")); err == nil {
		t.Fatal("a failed rename reported success")
	}
	if got, _ := s.Read("x", 0); string(got) != "original" {
		t.Fatalf("draft after failed write = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "workspaces", "x"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// TestStartersParse runs every starter through the real control agent's
// workspace/parse. It needs the real marshal binary.
func TestStartersParse(t *testing.T) {
	bin := os.Getenv("MARSHAL_BIN")
	if bin == "" {
		t.Skip("MARSHAL_BIN not set")
	}
	f := testFleet(t)
	f.newControl = func() (*Child, error) { return &Child{MarshalBin: bin}, nil }
	for _, name := range starterNames {
		_, _, diags, err := f.parseWorkspace(ctlContext(t), []byte(workspaceStarters[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, d := range diags {
			if d.Severity == "error" {
				t.Errorf("%s: line %d: %s", name, d.Line, d.Message)
			}
		}
	}
}

func TestFailInterruptedResetsBuildingVersions(t *testing.T) {
	s := NewTemplateStore(t.TempDir())
	for _, name := range []string{"a", "b"} {
		if _, err := s.Create(name, []byte("x"), "u"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Publish(name, "u"); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.SetBuild("a", 1, "building", "", 0, 0)
	_ = s.SetBuild("b", 1, "ok", "tag", 1, 1)
	got, err := s.FailInterrupted()
	if err != nil || len(got) != 1 || len(got["a"]) != 1 || got["a"][0] != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	if m, _ := s.Meta("a"); m.Versions[0].BuildStatus != "failed" {
		t.Fatalf("a = %+v", m.Versions[0])
	}
	if m, _ := s.Meta("b"); m.Versions[0].BuildStatus != "ok" {
		t.Fatalf("b = %+v", m.Versions[0])
	}
}
