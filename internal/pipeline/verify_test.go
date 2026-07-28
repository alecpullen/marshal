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
	fake.SetOutput("go test ./...", "ok  \tmarshal/internal/pipeline\t0.2s")
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
