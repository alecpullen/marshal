package plugins

import (
	"path/filepath"
	"testing"
)

func TestParseManifestAbsent(t *testing.T) {
	_, ok, err := ParseManifest(t.TempDir())
	if err != nil || ok {
		t.Fatalf("absent manifest: ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestParseManifestValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.toml"), "name = \"widgets\"\ndescription = \"Widget tools\"\n")
	m, ok, err := ParseManifest(dir)
	if err != nil || !ok {
		t.Fatalf("ParseManifest: ok=%v err=%v", ok, err)
	}
	if m.Name != "widgets" || m.Description != "Widget tools" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestParseManifestMalformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.toml"), "{{{ not toml")
	if _, _, err := ParseManifest(dir); err == nil {
		t.Fatal("malformed manifest should error")
	}
}

func TestParseManifestUnknownFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.toml"), "name = \"w\"\nfuture_field = 42\n")
	if _, ok, err := ParseManifest(dir); err != nil || !ok {
		t.Fatalf("unknown fields should be ignored: ok=%v err=%v", ok, err)
	}
}
