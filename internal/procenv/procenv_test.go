package procenv

import (
	"os"
	"os/exec"
	"testing"
)

func TestScrubMallocDebugDropsKnobsAndKeepsTheRest(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"MallocStackLogging=0",
		"HOME=/Users/x",
		"MallocScribble=1",
		"MallocStackLoggingNoCompact=0",
	}
	got := ScrubMallocDebug(env)
	want := []string{"PATH=/usr/bin", "HOME=/Users/x"}
	if len(got) != len(want) {
		t.Fatalf("ScrubMallocDebug = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ScrubMallocDebug = %v, want %v", got, want)
		}
	}
}

// A key that merely starts with a knob's name is not the knob.
func TestScrubMallocDebugKeepsLookalikes(t *testing.T) {
	env := []string{"MallocStackLoggingExtra=1", "NOT_MallocScribble=1"}
	if got := ScrubMallocDebug(env); len(got) != len(env) {
		t.Fatalf("ScrubMallocDebug dropped a lookalike key: %v", got)
	}
}

func TestScrubMallocDebugEnvUnsetsProcessEnv(t *testing.T) {
	t.Setenv("MallocStackLogging", "0")
	t.Setenv("MallocScribble", "1")
	t.Setenv("PROCENV_KEEP", "kept")

	ScrubMallocDebugEnv()

	if v, ok := os.LookupEnv("MallocStackLogging"); ok {
		t.Errorf("MallocStackLogging still set to %q", v)
	}
	if v, ok := os.LookupEnv("MallocScribble"); ok {
		t.Errorf("MallocScribble still set to %q", v)
	}
	if got := os.Getenv("PROCENV_KEEP"); got != "kept" {
		t.Errorf("PROCENV_KEEP = %q, want kept", got)
	}
}

// The point of scrubbing in-process is inheritance: a child spawned
// afterwards must not see the knob. This is the behaviour the TUI
// depends on, so assert it against a real child rather than the map.
func TestScrubbedEnvIsNotInheritedByChildren(t *testing.T) {
	t.Setenv("MallocStackLogging", "0")

	ScrubMallocDebugEnv()

	out, err := exec.Command("/bin/sh", "-c", `printf %s "${MallocStackLogging-unset}"`).Output()
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if got := string(out); got != "unset" {
		t.Fatalf("child saw MallocStackLogging=%q, want it unset", got)
	}
}
