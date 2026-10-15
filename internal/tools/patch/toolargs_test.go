package patch

import "testing"

func TestFromToolArgsFileEditKeepsMarkersVerbatim(t *testing.T) {
	old := "a\n=======\nb"
	repl := "c\n>>>>>>> REPLACE\nd"
	got, ok := FromToolArgs("file.edit", map[string]any{"path": "x.go", "old_string": old, "new_string": repl})
	if !ok || len(got) != 1 || got[0].Path != "x.go" || len(got[0].Chunks) != 1 {
		t.Fatalf("FromToolArgs = %+v, %v", got, ok)
	}
	if got[0].Chunks[0].Search != old || got[0].Chunks[0].Replace != repl {
		t.Fatalf("chunk altered: %+v", got[0].Chunks[0])
	}
}

func TestFromToolArgsWritePatchParses(t *testing.T) {
	got, ok := FromToolArgs("file.write_patch", map[string]any{"patch": "File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"})
	if !ok || len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("FromToolArgs = %+v, %v", got, ok)
	}
}

func TestFromToolArgsRejectsOtherToolsAndMissingFields(t *testing.T) {
	if _, ok := FromToolArgs("shell.run", map[string]any{"command": "ls"}); ok {
		t.Fatal("shell.run must not yield patches")
	}
	if _, ok := FromToolArgs("file.edit", map[string]any{"path": "x.go"}); ok {
		t.Fatal("file.edit without old_string must not yield patches")
	}
}
