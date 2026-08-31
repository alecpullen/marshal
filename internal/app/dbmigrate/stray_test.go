package dbmigrate

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptStrayProjectDBMovesStrayDatabase(t *testing.T) {
	root := t.TempDir()
	stray := filepath.Join(root, "internal", ".marshal")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "marshal.db"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AdoptStrayProjectDB(root, slog.Default()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal", "marshal.db")); err != nil {
		t.Fatalf("root database missing after adoption: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", ".marshal")); !os.IsNotExist(err) {
		t.Errorf("stray .marshal dir still exists: %v", err)
	}
}

func TestAdoptStrayProjectDBKeepsRootDatabase(t *testing.T) {
	root := t.TempDir()
	rootMarshal := filepath.Join(root, ".marshal")
	if err := os.MkdirAll(rootMarshal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootMarshal, "marshal.db"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(root, "sub", ".marshal")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "marshal.db"), []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AdoptStrayProjectDB(root, slog.Default()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(rootMarshal, "marshal.db"))
	if string(data) != "root" {
		t.Errorf("root database was overwritten with %q", data)
	}
}

func TestAdoptStrayProjectDBNoopWithoutStray(t *testing.T) {
	root := t.TempDir()
	if err := AdoptStrayProjectDB(root, slog.Default()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal")); !os.IsNotExist(err) {
		t.Error("created .marshal without a stray database")
	}
}