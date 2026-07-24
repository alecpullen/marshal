package rollover

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openFilesStateDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS file_reads (
		    session_id TEXT NOT NULL,
		    path TEXT NOT NULL,
		    read_at TEXT NOT NULL,
		    PRIMARY KEY(session_id, path)
		)`,
		`CREATE TABLE IF NOT EXISTS file_writes (
		    session_id TEXT NOT NULL,
		    path TEXT NOT NULL,
		    written_at TEXT NOT NULL,
		    PRIMARY KEY(session_id, path)
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// fakeRunner implements rollover.CommandRunner for testing.
type fakeRunner struct {
	byCmd    map[string]string // command -> stdout
	exitCode int
}

func (f *fakeRunner) Run(_ context.Context, req CommandRequest) (CommandResult, error) {
	out, ok := f.byCmd[req.Command]
	if !ok {
		return CommandResult{ExitCode: f.exitCode}, nil
	}
	return CommandResult{Stdout: out, ExitCode: 0}, nil
}

func TestFilesState_WrittenFiles(t *testing.T) {
	db := openFilesStateDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, p := range []string{"a.go", "b.go", "a.go"} { // dup -> deduped by PK
		if _, err := db.Exec(`INSERT OR IGNORE INTO file_writes(session_id,path,written_at) VALUES('s1',?,?)`, p, now); err != nil {
			t.Fatal(err)
		}
	}
	fs := NewFilesState(db, "s1", &fakeRunner{}, "/repo")
	got, err := fs.WrittenFiles()
	if err != nil {
		t.Fatalf("WrittenFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2 (deduped): %v", len(got), got)
	}
}

func TestFilesState_GitStatusShort(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{byCmd: map[string]string{
		"git status --short": " M a.go\n?? b.go",
	}}
	fs := NewFilesState(db, "s1", runner, "/repo")
	out, err := fs.GitStatusShort()
	if err != nil {
		t.Fatalf("GitStatusShort: %v", err)
	}
	if out != " M a.go\n?? b.go" {
		t.Errorf("got %q", out)
	}
}

func TestFilesState_GitStatusShortNoGit(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{exitCode: 128} // git exits 128 outside a repo
	fs := NewFilesState(db, "s1", runner, "/repo")
	_, err := fs.GitStatusShort()
	if !errors.Is(err, errNoGit) {
		t.Fatalf("err = %v, want errNoGit", err)
	}
}

func TestFilesState_OutstandingTodos(t *testing.T) {
	db := openFilesStateDB(t)
	runner := &fakeRunner{byCmd: map[string]string{
		"git grep -nE 'TODO|FIXME|XXX' -- ':*.go'": "a.go:10: TODO fix\nb.go:5: FIXME nil",
	}}
	fs := NewFilesState(db, "s1", runner, "/repo")
	out, err := fs.OutstandingTodos()
	if err != nil {
		t.Fatalf("OutstandingTodos: %v", err)
	}
	if !strings.Contains(out, "TODO fix") {
		t.Errorf("missing TODO, got %q", out)
	}
}
