package tunnels

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecOutputCarriesTheLastLineOfStderr(t *testing.T) {
	out, err := Exec().Output(context.Background(),
		[]string{"/bin/sh", "-c", "echo one >&2; echo two >&2; exit 3"})
	if err == nil {
		t.Fatalf("Output succeeded with %q", out)
	}
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("Output error = %T %v, want *CommandError", err, err)
	}
	if cmdErr.Code != 3 {
		t.Errorf("Code = %d, want 3", cmdErr.Code)
	}
	if cmdErr.Line != "two" {
		t.Errorf("Line = %q, want %q", cmdErr.Line, "two")
	}
}

func TestExecOutputAnswersStdout(t *testing.T) {
	out, err := Exec().Output(context.Background(), []string{"/bin/sh", "-c", "echo doc"})
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got, want := string(out), "doc\n"; got != want {
		t.Fatalf("Output = %q, want %q", got, want)
	}
}

func TestExecProcessStopsWithSIGTERM(t *testing.T) {
	p, err := Exec().Start([]string{"/bin/sh", "-c", "sleep 30"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3s")
	}
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("Done is not closed after Stop")
	}
}

func TestExecProcessReportsItsExit(t *testing.T) {
	p, err := Exec().Start([]string{"/bin/sh", "-c", "echo gone >&2; exit 255"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done is not closed")
	}
	var cmdErr *CommandError
	if !errors.As(p.Err(), &cmdErr) {
		t.Fatalf("Err = %T %v, want *CommandError", p.Err(), p.Err())
	}
	if cmdErr.Line != "gone" {
		t.Errorf("Line = %q, want %q", cmdErr.Line, "gone")
	}
	if cmdErr.Code != 255 {
		t.Errorf("Code = %d, want 255", cmdErr.Code)
	}
}
