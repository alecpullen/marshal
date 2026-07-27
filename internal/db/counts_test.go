package db

import "testing"

func TestCountFilesAndSymbols(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	if err := d.SaveFileIndex(projectID, []FileIndex{
		{Path: "a.go", Language: "go", Hash: "h1"},
		{Path: "b.go", Language: "go", Hash: "h2"},
		{Path: "c.md", Language: "markdown", Hash: "h3"},
	}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	if err := d.SaveSymbols(projectID, []Symbol{
		{FilePath: "a.go", Name: "Foo", Kind: "func"},
		{FilePath: "a.go", Name: "Bar", Kind: "func"},
	}); err != nil {
		t.Fatalf("SaveSymbols: %v", err)
	}

	files, err := d.CountFiles(projectID)
	if err != nil {
		t.Fatalf("CountFiles: %v", err)
	}
	if files != 3 {
		t.Errorf("CountFiles = %d, want 3", files)
	}

	syms, err := d.CountSymbols(projectID)
	if err != nil {
		t.Fatalf("CountSymbols: %v", err)
	}
	if syms != 2 {
		t.Errorf("CountSymbols = %d, want 2", syms)
	}
}

func TestCountsEmptyProject(t *testing.T) {
	d := newTestDB(t)
	projectID := mustCreateProject(t, d, "/repo")

	files, err := d.CountFiles(projectID)
	if err != nil || files != 0 {
		t.Errorf("CountFiles = (%d, %v), want (0, nil)", files, err)
	}
	syms, err := d.CountSymbols(projectID)
	if err != nil || syms != 0 {
		t.Errorf("CountSymbols = (%d, %v), want (0, nil)", syms, err)
	}
}
