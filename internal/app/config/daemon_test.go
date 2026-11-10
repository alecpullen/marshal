package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheIdleTimeoutDefaultsToThirtyMinutes pins the default the daemon's
// idle-session sweep assumes when the user never sets the key.
func TestTheIdleTimeoutDefaultsToThirtyMinutes(t *testing.T) {
	cfg := Default()
	if cfg.Daemon.IdleSessionTimeout != 30*time.Minute {
		t.Fatalf("Daemon.IdleSessionTimeout = %v, want 30m", cfg.Daemon.IdleSessionTimeout)
	}
}

// TestTheIdleTimeoutIsReadFromTheUserConfig pins the duration parsing, and
// that an explicit zero survives: zero means the daemon never ends an idle
// session, so it must not be confused with "unset" and replaced by the
// default.
func TestTheIdleTimeoutIsReadFromTheUserConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "duration", raw: "10m", want: 10 * time.Minute},
		{name: "zero", raw: "0", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			work := t.TempDir()
			writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
				"[daemon]\nidle_session_timeout = \""+tc.raw+"\"\n")

			cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Daemon.IdleSessionTimeout != tc.want {
				t.Fatalf("Daemon.IdleSessionTimeout = %v, want %v", cfg.Daemon.IdleSessionTimeout, tc.want)
			}
		})
	}
}

// TestABadIdleTimeoutFailsTheLoad pins that a malformed or negative value is
// a load failure naming the key, not a silent fallback to the default.
func TestABadIdleTimeoutFailsTheLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "unparseable", raw: "soon"},
		{name: "negative", raw: "-1m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			work := t.TempDir()
			writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
				"[daemon]\nidle_session_timeout = \""+tc.raw+"\"\n")

			_, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
			if err == nil {
				t.Fatalf("Load accepted idle_session_timeout = %q", tc.raw)
			}
			if !strings.Contains(err.Error(), "parse daemon.idle_session_timeout: ") {
				t.Fatalf("error = %v, want it to name parse daemon.idle_session_timeout", err)
			}
		})
	}
}

// TestTheIdleTimeoutRoundTripsThroughASave pins that a non-default value
// written by writeSections is read back by the next load.
func TestTheIdleTimeoutRoundTripsThroughASave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.Daemon.IdleSessionTimeout = 90 * time.Minute

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Daemon.IdleSessionTimeout != 90*time.Minute {
		t.Fatalf("Daemon.IdleSessionTimeout = %v after round trip, want 90m", loaded.Daemon.IdleSessionTimeout)
	}
}
