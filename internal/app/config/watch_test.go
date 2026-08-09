package config

import "testing"

func TestWatchEnabledDefaultsToFalse(t *testing.T) {
	if WatchEnabled(nil, true) {
		t.Fatal("WatchEnabled(nil, true) should default to false")
	}
	if WatchEnabled(nil, false) {
		t.Fatal("WatchEnabled(nil, false) should default to false")
	}
}

func TestWatchEnabledHonoursExplicitValue(t *testing.T) {
	tr := true
	if !WatchEnabled(&tr, false) {
		t.Fatal("explicit watch=true should enable the watcher")
	}
	f := false
	if WatchEnabled(&f, true) {
		t.Fatal("explicit watch=false should disable the watcher")
	}
}
