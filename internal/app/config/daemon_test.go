package config

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestDaemonWebSettingsAreReadFromTheUserConfig pins the five web and
// preview keys (one-daemon D10/D11): each is read from the user config as
// written.
func TestDaemonWebSettingsAreReadFromTheUserConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
		"[daemon]\n"+
			"web_listen = \"unix\"\n"+
			"web_group = \"marshal\"\n"+
			"web_url = \"https://marshal.example\"\n"+
			"preview_listen = \"127.0.0.1:7790\"\n"+
			"preview_url = \"https://preview.example\"\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "web_listen", got: cfg.Daemon.WebListen, want: "unix"},
		{name: "web_group", got: cfg.Daemon.WebGroup, want: "marshal"},
		{name: "web_url", got: cfg.Daemon.WebURL, want: "https://marshal.example"},
		{name: "preview_listen", got: cfg.Daemon.PreviewListen, want: "127.0.0.1:7790"},
		{name: "preview_url", got: cfg.Daemon.PreviewURL, want: "https://preview.example"},
	} {
		if tc.got != tc.want {
			t.Fatalf("Daemon.%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestDaemonWebSettingsRoundTripThroughASave pins that non-default values
// written by writeSections are read back by the next load.
func TestDaemonWebSettingsRoundTripThroughASave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.Daemon.WebListen = "/run/user/1000/marshal/web.sock"
	cfg.Daemon.WebGroup = "marshal"
	cfg.Daemon.WebURL = "https://marshal.example"
	cfg.Daemon.PreviewListen = "127.0.0.1:7790"
	cfg.Daemon.PreviewURL = "https://preview.example"

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "WebListen", got: loaded.Daemon.WebListen, want: cfg.Daemon.WebListen},
		{name: "WebGroup", got: loaded.Daemon.WebGroup, want: cfg.Daemon.WebGroup},
		{name: "WebURL", got: loaded.Daemon.WebURL, want: cfg.Daemon.WebURL},
		{name: "PreviewListen", got: loaded.Daemon.PreviewListen, want: cfg.Daemon.PreviewListen},
		{name: "PreviewURL", got: loaded.Daemon.PreviewURL, want: cfg.Daemon.PreviewURL},
	} {
		if tc.got != tc.want {
			t.Fatalf("Daemon.%s = %q after round trip, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestDaemonWebSettingsDefaultEmpty pins that the defaults serve nothing:
// the web listener, the preview listener and the two public addresses are
// unset until the user asks for them.
func TestDaemonWebSettingsDefaultEmpty(t *testing.T) {
	cfg := Default()
	for _, tc := range []struct {
		name string
		got  string
	}{
		{name: "WebListen", got: cfg.Daemon.WebListen},
		{name: "WebGroup", got: cfg.Daemon.WebGroup},
		{name: "WebURL", got: cfg.Daemon.WebURL},
		{name: "PreviewListen", got: cfg.Daemon.PreviewListen},
		{name: "PreviewURL", got: cfg.Daemon.PreviewURL},
	} {
		if tc.got != "" {
			t.Fatalf("Daemon.%s = %q, want empty", tc.name, tc.got)
		}
	}
}

// TestDaemonEgressIsReadFromTheUserConfig pins [daemon] egress, which turns
// the egress proxy sidecar on for container agents: off unless the user says
// so, and an explicit false stays false.
func TestDaemonEgressIsReadFromTheUserConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "on", raw: "true", want: true},
		{name: "off", raw: "false", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			work := t.TempDir()
			writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
				"[daemon]\negress = "+tc.raw+"\n")

			cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Daemon.Egress != tc.want {
				t.Fatalf("Daemon.Egress = %v, want %v", cfg.Daemon.Egress, tc.want)
			}
		})
	}
}

// TestDaemonEgressRoundTripsThroughASave pins that a non-default value
// written by writeSections is read back by the next load.
func TestDaemonEgressRoundTripsThroughASave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".marshal", "config.toml")

	cfg := Default()
	cfg.Daemon.Egress = true

	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(LoadOptions{WorkingDir: dir, HomeDir: filepath.Join(dir, "home")})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Daemon.Egress {
		t.Fatalf("Daemon.Egress = %v after round trip, want true", loaded.Daemon.Egress)
	}
}

// TestARetiredDaemonKeyIsNamed pins the retirement of [daemon] studio_addr and
// the join keys (one-daemon D10, D6): a file that still sets one is not read,
// but its key is named in DaemonConfig.Retired — sorted, and named once even
// when two layers set it — so a diagnostic can tell the user to drop it.
func TestARetiredDaemonKeyIsNamed(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
		"[daemon]\n"+
			"studio_addr = \"127.0.0.1:7788\"\n"+
			"join_url = \"https://relay.example.com\"\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"join_url", "studio_addr"}
	if !reflect.DeepEqual(cfg.Daemon.Retired, want) {
		t.Fatalf("Daemon.Retired = %v, want %v", cfg.Daemon.Retired, want)
	}

	// A project file sets one of them again: it is already named, so it is
	// not named twice, and join_name joins the list sorted.
	writeFile(t, filepath.Join(work, ".marshal", "config.toml"),
		"[daemon]\n"+
			"join_name = \"box\"\n"+
			"join_url = \"https://relay.example.com\"\n")

	cfg, err = Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load with a project config: %v", err)
	}
	want = []string{"join_name", "join_url", "studio_addr"}
	if !reflect.DeepEqual(cfg.Daemon.Retired, want) {
		t.Fatalf("Daemon.Retired = %v, want %v", cfg.Daemon.Retired, want)
	}
}

// TestARetiredDaemonKeyAloneWritesNoDaemonSection pins that the derived
// Retired list alone does not earn a [daemon] table. A user config that sets
// only a retired key — studio_addr here — leaves every real daemon value at
// its default, so a project save must not bake a default-valued [daemon]
// section into a shared, often-committed file, where it would pin today's
// defaults. A real value still writes the section.
func TestARetiredDaemonKeyAloneWritesNoDaemonSection(t *testing.T) {
	t.Run("a retired key alone", func(t *testing.T) {
		dir := t.TempDir()
		home := filepath.Join(dir, "home")
		work := filepath.Join(dir, "work")
		writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
			"[daemon]\nstudio_addr = \"127.0.0.1:7788\"\n")

		cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !reflect.DeepEqual(cfg.Daemon.Retired, []string{"studio_addr"}) {
			t.Fatalf("Daemon.Retired = %v, want the key the file still sets", cfg.Daemon.Retired)
		}

		path := filepath.Join(work, ".marshal", "config.toml")
		if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
			t.Fatalf("save: %v", err)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the saved config: %v", err)
		}
		text := string(saved)
		if strings.Contains(text, "[daemon]") {
			t.Errorf("the saved project config carries a [daemon] section for a retired key alone:\n%s", text)
		}
		for _, key := range []string{"idle_session_timeout", "egress", "web_listen", "web_group", "web_url", "preview_listen", "preview_url"} {
			if strings.Contains(text, key) {
				t.Errorf("the saved project config carries %q, want no daemon value:\n%s", key, text)
			}
		}
	})

	t.Run("a real value", func(t *testing.T) {
		dir := t.TempDir()
		home := filepath.Join(dir, "home")
		work := filepath.Join(dir, "work")
		writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
			"[daemon]\negress = true\n")

		cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		path := filepath.Join(work, ".marshal", "config.toml")
		if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
			t.Fatalf("save: %v", err)
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the saved config: %v", err)
		}
		text := string(saved)
		if !strings.Contains(text, "[daemon]") {
			t.Errorf("the saved project config carries no [daemon] section for egress = true:\n%s", text)
		}
		if !strings.Contains(text, "egress = true") {
			t.Errorf("the saved project config drops the real value:\n%s", text)
		}
	})
}

// TestASaveDropsTheRetiredDaemonKeys pins that a save never writes a retired
// key back to a file: the four only ever leave it.
func TestASaveDropsTheRetiredDaemonKeys(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "work")
	writeFile(t, filepath.Join(home, ".config", "marshal", "config.toml"),
		"[daemon]\n"+
			"studio_addr = \"127.0.0.1:7788\"\n"+
			"join_url = \"https://relay.example.com\"\n"+
			"join_name = \"box\"\n"+
			"join_ca_file = \"/etc/marshal/relay-ca.pem\"\n")

	cfg, err := Load(LoadOptions{HomeDir: home, WorkingDir: work})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Daemon.Retired) != 4 {
		t.Fatalf("Daemon.Retired = %v, want all four retired keys", cfg.Daemon.Retired)
	}

	path := filepath.Join(work, ".marshal", "config.toml")
	if err := SaveProjectConfig(path, cfg, Layers{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the saved config: %v", err)
	}
	for _, key := range []string{"studio_addr", "join_url", "join_name", "join_ca_file"} {
		if strings.Contains(string(saved), key) {
			t.Errorf("the saved config carries the retired key %q:\n%s", key, saved)
		}
	}
}
