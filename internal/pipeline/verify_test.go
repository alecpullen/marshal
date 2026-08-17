package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifierPasses(t *testing.T) {
	fake := NewFakeCommandRunner()
	fake.SetOutput("go build ./...", "")
	fake.SetOutput("go test ./...", "ok  	marshal/internal/pipeline	0.2s")
	v := Verifier{Build: "go build ./...", Test: "go test ./...", Timeout: time.Minute, Runner: fake}

	res, err := v.Run(context.Background(), "/work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.OK || res.Skipped {
		t.Fatalf("res = %+v, want OK", res)
	}
	if got := fake.Calls; len(got) != 2 || got[0] != "go build ./..." || got[1] != "go test ./..." {
		t.Errorf("calls = %v, want build then test", got)
	}
}

func TestVerifierBuildFailureSkipsTests(t *testing.T) {
	fake := NewFakeCommandRunner()
	fake.SetError("go build ./...", "internal/pipeline/plan.go:12: undefined: foo", errors.New("exit status 2"))
	v := Verifier{Build: "go build ./...", Test: "go test ./...", Timeout: time.Minute, Runner: fake}

	res, err := v.Run(context.Background(), "/work")
	if err != nil {
		t.Fatalf("Run returned an error for a failing command; want the failure in the result: %v", err)
	}
	if res.OK {
		t.Fatal("res.OK = true for a failing build")
	}
	if res.FailedCommand != "go build ./..." {
		t.Errorf("FailedCommand = %q", res.FailedCommand)
	}
	if res.Output == "" {
		t.Error("Output is empty; the fixer needs the compiler output")
	}
	if len(fake.Calls) != 1 {
		t.Errorf("calls = %v, want the test command skipped after a build failure", fake.Calls)
	}
}

func TestVerifierSkipsWhenNoCommands(t *testing.T) {
	v := Verifier{Runner: NewFakeCommandRunner()}
	res, err := v.Run(context.Background(), "/work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Skipped || !res.OK {
		t.Fatalf("res = %+v, want Skipped and OK", res)
	}
}

func TestDefaultVerifierDetectsGoModule(t *testing.T) {
	dir := t.TempDir()
	v := DefaultVerifier(dir, "", "", time.Minute)
	if v.Build != "" || v.Test != "" {
		t.Errorf("no go.mod: got %q/%q, want empty commands", v.Build, v.Test)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	v = DefaultVerifier(dir, "", "", time.Minute)
	if v.Build != "go build ./..." || v.Test != "go test ./..." {
		t.Errorf("go.mod present: got %q/%q, want the Go defaults", v.Build, v.Test)
	}
	v = DefaultVerifier(dir, "make build", "make test", time.Minute)
	if v.Build != "make build" || v.Test != "make test" {
		t.Errorf("configured commands were overridden: %q/%q", v.Build, v.Test)
	}
}

func TestResolveVerifyCommands(t *testing.T) {
	dir := t.TempDir()
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// configured wins
	got := ResolveVerifyCommands(dir, "make build", "make test")
	if got.Build != "make build" || got.Test != "make test" || !got.Known {
		t.Errorf("configured: %+v", got)
	}

	// go.mod → Go defaults
	must("go.mod", "module x\n")
	got = ResolveVerifyCommands(dir, "", "")
	if got.Build != "go build ./..." || got.Test != "go test ./..." || !got.Known {
		t.Errorf("go.mod: %+v", got)
	}

	// manifest-less → unknown, even with dominant go
	os.Remove(filepath.Join(dir, "go.mod"))
	must("a.go", "package a\n")
	got = ResolveVerifyCommands(dir, "", "")
	if got.Known {
		t.Errorf("manifest-less go should be unknown: %+v", got)
	}

	// empty dir → unknown
	got = ResolveVerifyCommands(t.TempDir(), "", "")
	if got.Known || got.Build != "" || got.Test != "" {
		t.Errorf("empty dir: %+v", got)
	}
}

func TestResolveNodeManifestScripts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"scripts":{"test":"jest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ResolveVerifyCommands(dir, "", "")
	if got.Test != "npm test" || got.Build != "" || !got.Known {
		t.Errorf("test-only package.json: %+v", got)
	}
}

func TestCLICommandRunnerKillsProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("process group test requires real process execution")
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := CLICommandRunner{}

	// Start a shell command that spawns a child process, then cancel.
	// If the process group is killed, both should be gone.
	dir := t.TempDir()
	// Write a script that spawns a child and waits.
	scriptPath := filepath.Join(dir, "spawn.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o755)

	start := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := runner.Run(ctx, dir, []string{"sh", scriptPath})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled command")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("command took %v, should have been killed within ~100ms", elapsed)
	}
}
