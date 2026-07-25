package sdd

import (
	"context"
	"strings"
	"testing"
)

func TestVerifyTaskPass(t *testing.T) {
	runner := NewFakeCommandRunner()
	runner.SetResult("go build ./...", "ok\n")
	runner.SetResult("go vet ./...", "")
	runner.SetResult("go test ./...", "ok marshal/internal/sdd\n")
	r := VerifyTask(context.Background(), runner, "/tmp/wt", 0)
	if !r.Pass {
		t.Fatalf("expected pass, got %+v (output=%q)", r, r.Output)
	}
	if r.Event != "VERIFY_PASS" {
		t.Errorf("Event = %q", r.Event)
	}
}

func TestVerifyTaskBuildFail(t *testing.T) {
	runner := NewFakeCommandRunner()
	runner.SetError("go build ./...", "exit status 1", "undefined: foo\n")
	r := VerifyTask(context.Background(), runner, "/tmp/wt", 0)
	if r.Pass {
		t.Fatal("expected fail")
	}
	if r.Event != "VERIFY_FAIL" {
		t.Errorf("Event = %q", r.Event)
	}
	if !strings.Contains(r.Output, "undefined: foo") {
		t.Errorf("Output = %q", r.Output)
	}
}

func TestVerifyTaskTestFail(t *testing.T) {
	runner := NewFakeCommandRunner()
	runner.SetResult("go build ./...", "")
	runner.SetResult("go vet ./...", "")
	runner.SetError("go test ./...", "exit status 1", "FAIL: TestFoo\n")
	r := VerifyTask(context.Background(), runner, "/tmp/wt", 0)
	if r.Pass {
		t.Fatal("expected fail on test failure")
	}
	if !strings.Contains(r.Output, "FAIL: TestFoo") {
		t.Errorf("Output = %q", r.Output)
	}
}

func TestVerifyTaskTimeout(t *testing.T) {
	runner := NewFakeCommandRunner()
	runner.SetError("go build ./...", context.DeadlineExceeded.Error(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 1) // 1 nanosecond
	defer cancel()
	r := VerifyTask(ctx, runner, "/tmp/wt", 0)
	if r.Pass {
		t.Fatal("expected fail on timeout")
	}
	if r.Event != "VERIFY_FAIL" {
		t.Errorf("Event = %q", r.Event)
	}
}
