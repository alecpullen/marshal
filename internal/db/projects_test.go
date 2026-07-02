package db

import (
	"strings"
	"testing"
)

func TestGetOrCreateProject(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	id1, err := db.GetOrCreateProject("/repo/path", "myproject")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	if id1 <= 0 {
		t.Fatalf("expected positive project id, got %d", id1)
	}

	id2, err := db.GetOrCreateProject("/repo/path", "different-name")
	if err != nil {
		t.Fatalf("GetOrCreateProject second call failed: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected same id for same root path, got %d and %d", id1, id2)
	}

	id3, err := db.GetOrCreateProject("/other/path", "myproject")
	if err != nil {
		t.Fatalf("GetOrCreateProject other path failed: %v", err)
	}
	if id3 == id1 {
		t.Fatalf("expected different id for different root path")
	}
}

func TestGetProject(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	rootPath := "/repo/path"
	name := "myproject"
	id, err := db.GetOrCreateProject(rootPath, name)
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	project, err := db.GetProject(id)
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if project.ID != id {
		t.Errorf("GetProject ID = %d, want %d", project.ID, id)
	}
	if project.RootPath != rootPath {
		t.Errorf("GetProject RootPath = %q, want %q", project.RootPath, rootPath)
	}
	if project.Name != name {
		t.Errorf("GetProject Name = %q, want %q", project.Name, name)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}

	_, err = db.GetProject(99999)
	if err == nil {
		t.Fatalf("GetProject not found: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "project not found") {
		t.Errorf("GetProject error = %q, want it to contain %q", err.Error(), "project not found")
	}
}
