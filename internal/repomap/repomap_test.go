package repomap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecpullen/marshal/internal/repomap"
)

// writeFile creates a file at path with content, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_GoFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func Hello(name string) string {
	return "Hello, " + name
}

func main() {
	Hello("world")
}
`)

	m, err := repomap.Build(dir, nil, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Sections) == 0 {
		t.Fatal("expected at least one section")
	}

	found := false
	for _, s := range m.Sections {
		for _, sym := range s.Symbols {
			if sym == "Hello" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected 'Hello' in map; sections: %+v", m.Sections)
	}
}

func TestBuild_CrossFileRefs(t *testing.T) {
	dir := t.TempDir()
	// pkg.go defines Greet; main.go references it.
	writeFile(t, filepath.Join(dir, "pkg", "greet.go"), `package pkg

func Greet(name string) string {
	return "hi " + name
}
`)
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "pkg"

func main() {
	pkg.Greet("world")
}
`)

	m, err := repomap.Build(dir, nil, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Sections) == 0 {
		t.Fatal("expected sections")
	}

	// Greet should appear somewhere in the map.
	found := false
	for _, s := range m.Sections {
		for _, sym := range s.Symbols {
			if sym == "Greet" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected 'Greet' in map; sections: %+v", m.Sections)
	}
}

func TestBuild_PersonalizedRank(t *testing.T) {
	dir := t.TempDir()

	// a.go defines A (referenced by b.go).
	writeFile(t, filepath.Join(dir, "a.go"), `package p
func Foo() {}
`)
	// b.go defines B and calls Foo.
	writeFile(t, filepath.Join(dir, "b.go"), `package p
func Bar() { Foo() }
`)
	// c.go is unrelated.
	writeFile(t, filepath.Join(dir, "c.go"), `package p
func Baz() {}
`)

	// With b.go as chat context, it should appear high in the ranked output.
	m, err := repomap.Build(dir, nil, repomap.Options{ChatFiles: []string{"b.go"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Sections) == 0 {
		t.Fatal("expected sections")
	}

	// b.go should be the first section.
	if m.Sections[0].Path != "b.go" {
		t.Errorf("expected b.go first, got %q", m.Sections[0].Path)
	}
}

func TestBuild_SkipsVendor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "vendor", "lib.go"), "package lib\nfunc VendorFunc() {}\n")

	m, err := repomap.Build(dir, nil, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range m.Sections {
		if strings.HasPrefix(s.Path, "vendor/") {
			t.Errorf("vendor file should not appear in map: %q", s.Path)
		}
	}
}

func TestBuild_Ignorer(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "secret.go"), "package main\nfunc Secret() {}\n")

	ig := &testIgnorer{patterns: []string{"secret.go"}}
	m, err := repomap.Build(dir, ig, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range m.Sections {
		if s.Path == "secret.go" {
			t.Errorf("ignored file should not appear in map")
		}
		for _, sym := range s.Symbols {
			if sym == "Secret" {
				t.Errorf("symbol from ignored file should not appear in map")
			}
		}
	}
}

func TestMap_String(t *testing.T) {
	m := &repomap.Map{
		Sections: []repomap.Section{
			{Path: "main.go", Symbols: []string{"main", "setup"}},
			{Path: "util/helper.go", Symbols: []string{"Helper"}},
		},
	}
	out := m.String()
	if !strings.Contains(out, "main.go:") {
		t.Error("expected main.go: in output")
	}
	if !strings.Contains(out, "│ main") {
		t.Error("expected │ main in output")
	}
	if !strings.Contains(out, "│ Helper") {
		t.Error("expected │ Helper in output")
	}
}

func TestMap_StringEmpty(t *testing.T) {
	m := &repomap.Map{}
	if m.String() != "" {
		t.Error("empty map should produce empty string")
	}
}

func TestPageRank_DanglingNodes(t *testing.T) {
	// Build a map where some files have no outgoing refs — should not panic.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc A() {}\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package p\nfunc B() {}\n")

	m, err := repomap.Build(dir, nil, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Sections) == 0 {
		t.Error("expected sections even with no cross-references")
	}
}

// testIgnorer is a simple Ignorer for tests.
type testIgnorer struct{ patterns []string }

func (ig *testIgnorer) Match(rel string) bool {
	for _, p := range ig.patterns {
		if rel == p {
			return true
		}
	}
	return false
}
