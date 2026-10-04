package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app/session"
)

func newFilesTest(t *testing.T) (*FilesManager, string) {
	t.Helper()
	root := t.TempDir()
	st := newWorktreeTestState(t, root)
	return NewFilesManager(func(id string) (*session.State, bool) { return st, id == "s1" }), root
}

func filesReq(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"sessionId": "s1", "path": path})
	return b
}

func TestFilesListsAndHidesGit(t *testing.T) {
	m, root := newFilesTest(t)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "zdir"), 0o755)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("bb"), 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	got, err := m.Files(context.Background(), filesReq(""))
	if err != nil {
		t.Fatal(err)
	}
	entries := got.(map[string]any)["entries"].([]FileEntry)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "zdir,a.txt,b.txt" {
		t.Fatalf("names = %v", names)
	}
}

func TestFilesRejectsEscapes(t *testing.T) {
	m, root := newFilesTest(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	for _, p := range []string{"../x", "/etc/passwd", "link/secret"} {
		if _, err := m.File(context.Background(), filesReq(p)); err == nil {
			t.Errorf("File(%q) succeeded, want rejection", p)
		}
	}
}

func TestFileBinaryAndTruncation(t *testing.T) {
	m, root := newFilesTest(t)
	os.WriteFile(filepath.Join(root, "bin"), []byte("a\x00b"), 0o644)
	os.WriteFile(filepath.Join(root, "big"), []byte(strings.Repeat("a", maxFileView+10)), 0o644)
	got, err := m.File(context.Background(), filesReq("bin"))
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(map[string]any); r["binary"] != true || r["content"] != "" {
		t.Fatalf("bin = %v", r)
	}
	got, err = m.File(context.Background(), filesReq("big"))
	if err != nil {
		t.Fatal(err)
	}
	r := got.(map[string]any)
	if r["truncated"] != true || len(r["content"].(string)) != maxFileView {
		t.Fatalf("big truncated=%v len=%d", r["truncated"], len(r["content"].(string)))
	}
}
