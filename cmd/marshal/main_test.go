package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"marshal/internal/app"
)

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
