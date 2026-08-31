package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureMarshalDirIgnoredCreatesNestedIgnore(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureMarshalDirIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".marshal", ".gitignore"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"marshal.db", "marshal.log", "tool-results/", "pipeline/"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("nested .gitignore missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "config.toml") {
		t.Error("nested .gitignore must not exclude config.toml")
	}
}

func TestEnsureMarshalDirIgnoredIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureMarshalDirIgnored(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	path := filepath.Join(dir, ".marshal", ".gitignore")
	if err := os.WriteFile(path, []byte("# custom\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureMarshalDirIgnored(dir); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "# custom\n" {
		t.Errorf("user additions clobbered: %q", data)
	}
}