package session

import "testing"

func TestWatchResumeLatchSetTakeClears(t *testing.T) {
	state := newTestState() // same helper as watch_reports_test.go (session_test.go:25)
	if _, _, ok := state.TakeWatchResume(); ok {
		t.Fatal("fresh session had an armed latch")
	}
	state.SetWatchResume("build", true)
	name, repeat, ok := state.TakeWatchResume()
	if !ok || name != "build" || !repeat {
		t.Fatalf("take = (%q, %v, %v), want (build, true, true)", name, repeat, ok)
	}
	if _, _, ok := state.TakeWatchResume(); ok {
		t.Fatal("take did not clear the latch")
	}
}

func TestWatchResumeLatchReplacesOnWrite(t *testing.T) {
	state := newTestState()
	state.SetWatchResume("first", false)
	state.SetWatchResume("second", true)
	name, repeat, ok := state.TakeWatchResume()
	if !ok || name != "second" || !repeat {
		t.Fatalf("take = (%q, %v, %v), want latest arm (second, true, true)", name, repeat, ok)
	}
}
