package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/app/config"
	"marshal/web/bridge"
)

func TestThePathsAreUnderTheDaemonDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MARSHAL_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")

	dir := config.DaemonDir(home)
	got := PathsFor(home)

	if got.Dir != dir {
		t.Errorf("Dir = %q, want %q", got.Dir, dir)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"Socket", got.Socket, filepath.Join(dir, "marshal.sock")},
		{"StateDir", got.StateDir, filepath.Join(dir, "state")},
		{"Registry", got.Registry, filepath.Join(dir, "fleet.json")},
		{"Log", got.Log, filepath.Join(dir, "webbridge.log")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
		if filepath.Dir(tc.got) != dir {
			t.Errorf("%s = %q is not under %q", tc.name, tc.got, dir)
		}
	}
	if got.Socket != config.DaemonSocketPath(home) {
		t.Errorf("Socket = %q, want DaemonSocketPath %q", got.Socket, config.DaemonSocketPath(home))
	}
}

// TestTheBridgeAndConfigAgreeOnTheDaemonDir pins the two copies of the
// data-dir rule together: web/ may not import internal/, so the bridge
// restates it (decision 2).
func TestTheBridgeAndConfigAgreeOnTheDaemonDir(t *testing.T) {
	home := "/home/u"

	for _, tc := range []struct {
		name string
		data string
		xdg  string
	}{
		{name: "marshal data dir", data: "/custom/data"},
		{name: "xdg data home", xdg: "/xdg/data"},
		{name: "home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("MARSHAL_DATA_DIR", tc.data)
			t.Setenv("XDG_DATA_HOME", tc.xdg)

			fromBridge, err := bridge.DefaultDaemonDir()
			if err != nil {
				t.Fatalf("bridge.DefaultDaemonDir(): %v", err)
			}
			if fromConfig := config.DaemonDir(home); fromBridge != fromConfig {
				t.Errorf("bridge.DefaultDaemonDir() = %q, config.DaemonDir() = %q", fromBridge, fromConfig)
			}
		})
	}
}

func TestFindWebbridgePrefersTheOverrideThenTheSibling(t *testing.T) {
	// An override wins outright, even when it is not the sibling.
	override := filepath.Join(t.TempDir(), "beside-webridge")
	writeExecutable(t, override)

	got, err := FindWebbridge(override)
	if err != nil {
		t.Fatalf("FindWebbridge(override): %v", err)
	}
	if got != override {
		t.Errorf("FindWebbridge(override) = %q, want %q", got, override)
	}

	// A non-executable override is refused rather than run.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("not a program\n"), 0o644); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	if _, err := FindWebbridge(plain); err == nil {
		t.Errorf("FindWebbridge(%q) accepted a file that is not executable", plain)
	}
	if _, err := FindWebbridge(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Errorf("FindWebbridge accepted an override that does not exist")
	}
}

// writeExecutable writes a placeholder program at path.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("make %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
