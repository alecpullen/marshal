package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/app"
	"marshal/internal/trust"
)

// TestRecordPermanentTrustAnchorsAtRepoRoot pins the --trust fix: the
// record must be keyed on the canonical repository root, not the launch
// subdirectory, so the next root-anchored launch finds it. The stored
// config hash must be taken at the root too — a subdirectory hash is
// always empty (no .marshal/config.toml there), which would force a
// re-prompt on the next launch even with the right key.
func TestRecordPermanentTrustAnchorsAtRepoRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git entry makes root a repository for repo.Root.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A project config at the root gives the record a real hash.
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marshal", "config.toml"), []byte("[agent]\nplan_first = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", dataDir)

	if err := recordPermanentTrustIn(sub); err != nil {
		t.Fatalf("recordPermanentTrustIn: %v", err)
	}

	records, err := trust.NewStore(dataDir).Load()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	wantKey := trust.Canonicalize(root)
	rec, ok := records[wantKey]
	if !ok {
		keys := make([]string, 0, len(records))
		for k := range records {
			keys = append(keys, k)
		}
		t.Fatalf("no trust record for repo root %q; records: %v", wantKey, keys)
	}
	if !rec.Trusted {
		t.Error("record is not trusted")
	}
	wantHash, err := trust.ConfigHashFor(root)
	if err != nil {
		t.Fatalf("ConfigHashFor(root): %v", err)
	}
	if rec.ConfigHash != wantHash {
		t.Errorf("config hash = %q, want the root config hash %q", rec.ConfigHash, wantHash)
	}
}

// TestRecordPermanentTrustNonGitDirUsesLaunchDir pins the non-git
// fallback: outside a repository the record keys on the launch directory,
// exactly as before.
func TestRecordPermanentTrustNonGitDirUsesLaunchDir(t *testing.T) {
	dir := t.TempDir()
	dataDir := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", dataDir)

	if err := recordPermanentTrustIn(dir); err != nil {
		t.Fatalf("recordPermanentTrustIn: %v", err)
	}

	records, err := trust.NewStore(dataDir).Load()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	if _, ok := records[trust.Canonicalize(dir)]; !ok {
		t.Fatalf("no trust record for the launch directory %q", trust.Canonicalize(dir))
	}
}

func TestRunDispatchesACPSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldACP := acpRunner
	defer func() { acpRunner = oldACP }()
	acpRunner = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
		called = true
		return nil
	}
	if err := run(context.Background(), []string{"acp"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !called {
		t.Fatal("acpRunner was not called")
	}
}

func TestACPListenFlagRoutesToListener(t *testing.T) {
	var gotNetwork, gotAddr string
	orig := acpListener
	acpListener = func(ctx context.Context, network, addr string, stderr io.Writer) error {
		gotNetwork, gotAddr = network, addr
		return nil
	}
	t.Cleanup(func() { acpListener = orig })

	args := []string{"acp", "--listen", "unix:///run/marshal/agent.sock"}
	if err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotNetwork != "unix" || gotAddr != "/run/marshal/agent.sock" {
		t.Fatalf("got (%q, %q), want (unix, /run/marshal/agent.sock)", gotNetwork, gotAddr)
	}
}

func TestRunPrintsVersionAndSkipsApp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	oldApp := appRunner
	defer func() { appRunner = oldApp }()
	appRunner = func(ctx context.Context, out io.Writer, opts ...app.Option) error {
		called = true
		return nil
	}

	oldVersion, oldCommit, oldDate := version, commit, date
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()
	version, commit, date = "0.0.1-alpha", "abc1234", "2026-08-26T00:00:00Z"

	if err := run(context.Background(), []string{"--version"}, bytes.NewBuffer(nil), &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if called {
		t.Fatal("appRunner was called; --version must not start the TUI")
	}
	out := stdout.String()
	for _, want := range []string{"marshal", "0.0.1-alpha", "abc1234", "2026-08-26T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

func TestVersionStringFallsBackToDev(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	defer func() { version, commit, date = oldVersion, oldCommit, oldDate }()
	version, commit, date = "", "", ""

	got := versionString()
	if !strings.Contains(got, "dev") {
		t.Errorf("versionString() = %q, want it to mention %q", got, "dev")
	}
}
